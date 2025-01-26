set -o errexit
set -o nounset
set -o pipefail

script_dir=$(cd $(dirname $0) && pwd)

# 提示用户输入 relayNodes
read -p "请输入 relayNodes（用逗号分隔）: " relayNodesInput

# 将输入的字符串转换为数组
IFS=',' read -r -a relayNodesArray <<< "$relayNodesInput"

read -p "请输入 relayNodes 的ip地址（用逗号分隔）: " relayNodesIpInput

IFS=',' read -r -a relayNodesIpArray <<< "$relayNodesIpInput"

# 检测输入的 relayNodes 和 relayNodesIp 是否一一对应
if [ ${#relayNodesArray[@]} -ne ${#relayNodesIpArray[@]} ]; then
    echo "relayNodes 和 relayNodesIp 的数量不一致"
    exit 1
fi

# 按照逗号连接数组元素
relayNodes=$(IFS=,; echo "${relayNodesArray[*]}")
relayNodesIp=$(IFS=,; echo "${relayNodesIpArray[*]}")

# 让用户确认输入的 relayNodes 和 relayNodesIp
echo "relayNodes: ${relayNodes}"
echo "relayNodesIp: ${relayNodesIp}"
read -p "请确认 relayNodes 和 relayNodesIp 是否正确（y/n）: " confirm
if [ "$confirm" != "y" ]; then
    echo "用户取消"
    exit 1
fi

# 检测helm和openssl是否安装
if ! command -v helm &> /dev/null; then
    echo "helm 未安装"
    exit 1
fi

if ! command -v openssl &> /dev/null; then
    echo "openssl 未安装"
    exit 1
fi

# 生成psk
set -x
psk=$(openssl rand -base64 32)
set +x

echo "psk: ${psk}"

# 部署edgemesh
# 初始化 helm install 命令
helm_command="helm install edgemesh --namespace kubeedge --set agent.psk=${psk}"

# 迭代 relayNodesArray 和 relayNodesIpArray 并生成命令
for i in "${!relayNodesArray[@]}"; do
    nodeName="${relayNodesArray[$i]}"
    advertiseAddress="${relayNodesIpArray[$i]}"
    helm_command+=" --set agent.relayNodes[$i].nodeName=$nodeName,agent.relayNodes[$i].advertiseAddress=\"{$advertiseAddress}\""
done

# 添加 helm chart URL
helm_command+=" ${script_dir}/edgemesh.tgz"

# 输出生成的命令
echo "helm_command: ${helm_command}"
eval $helm_command