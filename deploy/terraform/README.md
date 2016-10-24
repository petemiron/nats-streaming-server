# Overview
 
Creates a base NATS server, VPC and security groups.

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

# LICENSE MIT
