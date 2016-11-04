provider "aws" {
region = "${var.region}"
}
module "vpc" {
  source        = "./vpc"
  name          = "web"
  cidr          = "10.0.0.0/16"
  public_subnet = "10.0.1.0/24"
}

data "template_file" "config" {
  template = "${file("files/bootstrap.sh")}"

  vars {
    efs_mount_target_ip = "${aws_efs_mount_target.nats-service-efs.ip_address}"
  }
}

output "bootstrap.sh" {
  value = "${data.template_file.config.rendered}"
}

resource "aws_autoscaling_group" "asg_app" {
  lifecycle { create_before_destroy = true }

  # spread the app instances across the availability zones
  # availability_zones = ["${split(",", var.availability_zones)}"]

  # interpolate the LC into the ASG name so it always forces an update
  name = "asg-app - ${aws_launch_configuration.web.name}"
  max_size = 1
  min_size = 1
  # wait_for_elb_capacity = 1 
  desired_capacity = 1 
  health_check_grace_period = 300
  health_check_type = "EC2"
  launch_configuration = "${aws_launch_configuration.web.id}"
  # load_balancers = ["${aws_elb.elb_app.id}"]
  vpc_zone_identifier = ["${module.vpc.public_subnet_id}"]
}

resource "aws_launch_configuration" "web" {
  image_id = "${lookup(var.ami, var.region)}" 
  instance_type = "${var.instance_type}"
  key_name = "${var.key_name}"
  associate_public_ip_address=true
  user_data = "${data.template_file.config.rendered}"

  security_groups = [ 
    "${aws_security_group.nats_service_host_group.id}",
    "${aws_security_group.nats_service_custom_group.id}",
    "${aws_security_group.ec2.id}",
  ]

  count = 1
}
