# Deploy NATS Streaming Server on AWS

1. Create a VPC for NATS server and clients
1. Create an Elastic File System in that VPC
1. Create a Security Group for Elastic File System Access
1. Create a Security Group for NATS Client/Server Communication
1. Create an EC2 instance for the server
1. Install NATS streaming
1. Run NATS Streaming Server
1. Create an EC2 instance for a test client
1. Run Benchmark to test

# Run cloud formation to create stack
This cloud formation template creates an instance and mounts an EFS file system. Based on the [AWS sample template](http://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/quickref-efs.html)

``` bash
aws cloudformation create-stack --stack-name [stack-name] --template-body file://nats-streaming-efs-template.json --capabilities CAPABILITY_IAM
```

# TODO:
1. Create NATS AMI to use by default.
1. Add in Elastic IP (or Application Load Balancer) for client failover to new instance.
1. Add Benchmark tests for performance of EFS.



