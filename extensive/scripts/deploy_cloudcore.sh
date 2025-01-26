set -o errexit
set -o nounset
set -o pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "script_dir: $script_dir"
source "$script_dir/utils.sh"
utils::fetch_yq

KUBEEDGE_ROOT=$(unset CDPATH && cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
echo "KUBEEDGE_ROOT: $KUBEEDGE_ROOT"
cd $KUBEEDGE_ROOT
make all WHAT=cloudcore BUILD_WITH_CONTAINER=false
_output/local/bin/cloudcore --defaultconfig > cloudcore.yaml

$script_dir/yq eval '.kubeAPIConfig.kubeConfig = "/etc/rancher/k3s/k3s.yaml"' -i cloudcore.yaml
$script_dir/yq eval '.modules.dynamicController.enable = true' -i cloudcore.yaml

# get ip from cloudcore.yaml
IP=$(awk '/advertiseAddress:/{getline; print $2}' cloudcore.yaml)
echo "ip: $IP"

# copy cloudcore to /usr/local/bin
sudo cp _output/local/bin/cloudcore /usr/local/bin
sudo cp build/tools/cloudcore.service /etc/systemd/system

# copy config file
sudo mkdir -p /etc/kubeedge/config
sudo mv cloudcore.yaml /etc/kubeedge/config

sudo systemctl daemon-reload
sudo systemctl start cloudcore
sudo systemctl enable cloudcore
systemctl status cloudcore
