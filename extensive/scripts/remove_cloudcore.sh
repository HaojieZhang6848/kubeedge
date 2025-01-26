sudo systemctl stop cloudcore
sudo systemctl disable cloudcore

sudo rm -f /usr/local/bin/cloudcore
sudo rm -f /etc/systemd/system/cloudcore.service

sudo rm -rf /etc/kubeedge
sudo rm -rf /var/lib/kubeedge

sudo systemctl daemon-reload