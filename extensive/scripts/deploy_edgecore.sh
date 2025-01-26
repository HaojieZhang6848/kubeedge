set -o errexit
set -o nounset
set -o pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "script_dir: $script_dir"
source "$script_dir/utils.sh"
utils::fetch_yq

if [ -z "${CLOUD_IP+x}" ]; then
    echo "CLOUD_IP is unset. Please set the CLOUD_IP environment variable."
    exit 1
fi

if [ -z "${CLOUD_TOKEN+x}" ]; then
    echo "CLOUD_TOKEN is unset. Please set the CLOUD_TOKEN environment variable."
    exit 1
fi

KUBEEDGE_ROOT=$(unset CDPATH && cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
echo "KUBEEDGE_ROOT: $KUBEEDGE_ROOT"
cd $KUBEEDGE_ROOT

# 构建 edgecore
make all WHAT=edgecore BUILD_WITH_CONTAINER=false

# 输出默认配置文件
_output/local/bin/edgecore --defaultconfig > edgecore.yaml

# sed命令，替换默认配置文件中的字段。其实应该用yq之类的工具来处理yaml文件，但是这里不想有额外的依赖
# 替换CLOUD_IP字段
$script_dir/yq eval '.modules.edgeHub.httpServer = "https://'$CLOUD_IP':10002"' -i edgecore.yaml
$script_dir/yq eval '.modules.edgeHub.quic.server = "'$CLOUD_IP':10001"' -i edgecore.yaml
$script_dir/yq eval '.modules.edgeHub.websocket.server = "'$CLOUD_IP':10000"' -i edgecore.yaml
$script_dir/yq eval '.modules.edgeStream.server = "'$CLOUD_IP':10004"' -i edgecore.yaml

# 替换CLOUD_TOKEN字段
$script_dir/yq eval '.modules.edgeHub.token = "'$CLOUD_TOKEN'"' -i edgecore.yaml

# 替换mqttmode为0
$script_dir/yq eval '.modules.eventBus.mqttMode = 0' -i edgecore.yaml

# 设置metaserver(edgemesh要用)
$script_dir/yq eval '.modules.metaManager.metaServer.server = "0.0.0.0:10550"' -i edgecore.yaml
$script_dir/yq eval '.modules.metaManager.metaServer.enable = true' -i edgecore.yaml

# # 设置clusterDNS(edgemesh要用)
$script_dir/yq eval '.modules.edged.tailoredKubeletConfig.clusterDNS += ["169.254.96.16"]' -i edgecore.yaml

# 设置cgroupDriver
$script_dir/yq eval '.modules.edged.tailoredKubeletConfig.cgroupDriver = "systemd"' -i edgecore.yaml

sudo mkdir -p /etc/kubeedge/config
sudo mv edgecore.yaml /etc/kubeedge/config

# 拷贝服务文件和二进制文件
sudo cp build/tools/edgecore.service /etc/systemd/system
sudo cp _output/local/bin/edgecore /usr/local/bin

sudo systemctl daemon-reload
sudo systemctl start edgecore
sudo systemctl enable edgecore
systemctl status edgecore

