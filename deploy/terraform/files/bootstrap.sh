#!/bin/bash 
set +x
echo "Bootstrap started" > bootstrap.log
sudo mkdir /nats-data; 
sudo mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2 ${efs_mount_target_ip}:/ /nats-data

# per instance setup of systemd template
NATS_DATA_ROOT=/nats-data/

INSTANCE_ID=2
SERVICE_NAME=natssd
SVC_ID=$SERVICE_NAME-$INSTANCE_ID
sudo useradd -m -d /nats-data/$SVC_ID -s /usr/sbin/nologin -c "$SVC_ID service user." \
    -u $((1200+$INSTANCE_ID)) $SVC_ID

# copy everything in ./home/ to /home/service_user
sudo rsync -azvh home/ubuntu/home/ /$NATS_DATA_ROOT/$SVC_ID
sudo chown $SVC_ID:$SVC_ID -R /$NATS_DATA_ROOT/$SVC_ID

sudo systemctl enable $service_name\@$instance_id.service
sudo systemctl start $SERVICE_NAME\@$INSTANCE_ID.service

echo "Bootstrap finished" > bootstrap.log
