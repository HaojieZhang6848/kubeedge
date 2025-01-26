set -o errexit
set -o nounset
set -o pipefail

sudo systemctl stop edgecore
sudo systemctl disable edgecore
sudo rm -f /etc/systemd/system/edgecore.service
sudo systemctl daemon-reload

sudo rm -f /usr/local/bin/edgecore
sudo rm -rf /etc/kubeedge
sudo rm -f /etc/kubeedge/config/edgecore.yaml
sudo rm -rf /var/lib/kubeedge