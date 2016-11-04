module "nats" {
  source = "../nats/"
  region = "us-east-1"
  ami = {
    # this is a custom AMI built using the nats-aws-ubuntu-16.04.json packer build file
    us-east-1 = "ami-e6e7c1f1",
    us-west-2 = "ami-331fbe53"
  }
}

output "ELB Public Name" {
  value = "${module.nats.elb_dns}"
}
