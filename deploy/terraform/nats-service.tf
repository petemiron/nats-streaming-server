provider "aws" {
region = "${var.region}"
}
module "vpc" {
  source        = "./vpc"
  name          = "web"
  cidr          = "10.0.0.0/16"
  public_subnet = "10.0.1.0/24"
}
resource "aws_instance" "web" {
  ami = "${lookup(var.ami, var.region)}" 
  instance_type = "${var.instance_type}"
  key_name = "${var.key_name}"
  private_ip = "${lookup(var.instance_ips, count.index)}"
  subnet_id = "${module.vpc.public_subnet_id}"
  associate_public_ip_address=true
  # user_data = "${file("files/test_bootstrap.sh")}"

  provisioner "remote-exec" {
    connection {
      user = "ubuntu"
      private_key = "${file("~/.ssh/aws.pem")}"
    },
    inline = [
      "sudo mkdir /efs",
      "sudo mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2 ${aws_efs_mount_target.nats-service-efs.ip_address}:/ /efs",
    ],
  }

  vpc_security_group_ids = [ 
    "${aws_security_group.nats_service_host_group.id}",
    "${aws_security_group.nats_service_custom_group.id}",
    "${aws_security_group.ec2.id}",
  ]

  # availability_zone = "${var.avail_zone[count.index]}"
  tags {
         Name = "web-${count.index}"
  }
  count = 1
}
