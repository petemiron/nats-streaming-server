output "addresses" {
  value = ["${aws_launch_configuration.web.*.public_ip}"]
}
output "public_subnet_id" {
  value = "${module.vpc.public_subnet_id}"
}
