# Overview
Creates a base NATS server, VPC and security groups.

# Build your packer AMI
First, you need to build your NATS aws packer [ami](../packer/ubuntu/packer-templates/). 
Using that AMI, update terraform.tfvars with the correct AMIs.

```
region = "us-east-1"
ami = {
  # this is a custom AMI built using the nats-aws-ubuntu-16.04.json packer build file
  us-east-1 = "ami-[Your New AMI ID]"
}

```

# Securing

To prevent instance ssh and admin consoles being accessible from any IP address

Get your local IP Address:

``` bash
curl http://checkip.amazonaws.com/
```

Then, use that network to set the admin_cidr var list. Or update with your own management network and appropriate CIDR.


# Plan the terraform

``` bash
terraform plan -var 'admin_cidr=["[ip_address]/1"]'

```

# Build the terraform 

Warning: this will create billable AWS resources.
 
``` bash
terraform apply -var 'admin_cidr=["[ip_address]/1"]'
```

# LICENSE
MIT
