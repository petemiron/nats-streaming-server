#!/bin/bash 
echo "STARTING BOOT" > boot.txt
sudo mkdir /efs; 
sudo mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2 ${efs_mount_target_ip}:/ /efs
echo "FINISHED BOOT" >> boot.txt
