# Mode 3 Postgres Terraform module

Stand up the PostgreSQL database that backs sparkwing's
[Mode 3](../../../docs/deployment-modes.md) (Postgres + object storage)
with one `terraform apply`. The module provisions the database, its
security group, and its subnet group, and writes the connection string
to AWS Secrets Manager so each runner reads one secret instead of
hand-rolling a DSN.

This is the database half of Mode 3. Caches and logs still live in an
object store you point the profile at; this module does not create the
bucket.

## What it creates

- A PostgreSQL database, either a single RDS instance (the default,
  cheapest at idle) or an Aurora Serverless v2 cluster that scales
  capacity with load. One knob, `engine`, picks between them.
- A security group that admits the PostgreSQL port only from the runner
  hosts you name (`allowed_security_group_ids` or `allowed_cidr_blocks`).
- A DB subnet group across the private subnets you supply.
- A Secrets Manager secret holding the full connection string, for the
  runner's `SPARKWING_PG_URL`.

## What it expects from you

The module places the database into networking you already have; it does
not build a VPC. Supply:

- `vpc_id`: the VPC the database lives in.
- `subnet_ids`: at least two private subnets in different availability
  zones (both RDS and Aurora require two AZs).
- One of `allowed_security_group_ids` or `allowed_cidr_blocks`: who may
  reach the database. Point this at your runner hosts. With neither set,
  nothing can connect.

The database is never publicly accessible: runners reach it from inside
the VPC (a peered network, a VPN, or hosts in the same VPC).

## Usage

```hcl
module "sparkwing_db" {
  source = "github.com/sparkwing-dev/sparkwing//install/terraform/mode3-postgres"

  name       = "sparkwing"
  vpc_id     = "vpc-0abc123"
  subnet_ids = ["subnet-0aaa", "subnet-0bbb"]

  allowed_security_group_ids = ["sg-0runner111"]

  # Default engine = "rds" with a db.t4g.micro. For autoscaling capacity:
  # engine                  = "aurora-serverless-v2"
  # serverless_min_capacity = 0.5
  # serverless_max_capacity = 8
}

output "sparkwing_dsn_secret" {
  value = module.sparkwing_db.dsn_secret_arn
}
```

```sh
terraform init
terraform apply
```

See `terraform.tfvars.example` for a copy-paste starting point, and
`variables.tf` for every knob with its default.

## Pointing a runner at Mode 3

After `apply`, read the connection string from the secret named in the
`dsn_secret_arn` / `dsn_secret_name` output and export it on each runner:

```sh
export SPARKWING_PG_URL="$(aws secretsmanager get-secret-value \
  --secret-id sparkwing/postgres-dsn \
  --query SecretString --output text)"
```

Then add a Mode 3 profile that reads the DSN from that variable:

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  shared:
    state:
      type: postgres
      url_source: env:SPARKWING_PG_URL
    cache:
      type: s3
      bucket: my-org-sparkwing
      prefix: cache
    logs:
      type: s3
      bucket: my-org-sparkwing
      prefix: logs
```

```sh
sparkwing run hello --profile shared
sparkwing-web --state-spec=postgres://...  # same DSN
```

The first runner to start migrates the schema; staggered upgrades are
covered under "Schema versioning" in
[deployment-modes.md](../../../docs/deployment-modes.md).

## Teardown

`deletion_protection` defaults to true and a final snapshot is taken on
destroy. To tear down a throwaway stack, set `deletion_protection = false`
and `skip_final_snapshot = true`, apply, then `terraform destroy`.

## Verification

### Offline plan gate (no AWS account)

`bin/check-terraform.sh` runs in the pre-push gate and is runnable by
hand from the repo root:

```sh
bash bin/check-terraform.sh
```

It runs `terraform fmt -check -recursive` and `terraform validate` on the
module, then plans the `test/plan` fixture for both engine knobs and
asserts the resource set per knob. The fixture (`test/plan/main.tf`)
configures the AWS provider with mock credentials and every skip flag, so
`terraform plan` enumerates the full resource graph without an AWS account
or any API call.

The asserted plans, which cover the networking wiring and the count-gated
engine branch flipping correctly:

- `engine = "rds"` plans 6 resources: `random_password.master`,
  `aws_db_subnet_group.this`, `aws_security_group.this`,
  `aws_db_instance.this[0]`, `aws_secretsmanager_secret.dsn`,
  `aws_secretsmanager_secret_version.dsn`. No Aurora cluster.
- `engine = "aurora-serverless-v2"` plans 7 resources: the same subnet
  group, security group, password, and secret pair, plus
  `aws_rds_cluster.this[0]` and `aws_rds_cluster_instance.this[0]`. No
  `aws_db_instance`.

The plan asserts the resource graph's shape. The security posture lives in
attribute values, which `bin/check-terraform.sh` also asserts via
`terraform test` (`tests/security.tftest.hcl`) with mocked providers, again
with no AWS account or API call:

- `publicly_accessible = false` on the RDS instance and the Aurora cluster
  instance.
- `storage_encrypted = true` on the RDS instance and the Aurora cluster.
- Security-group ingress is restricted to the PostgreSQL port (5432) over
  tcp, opens no CIDR when sourced from a security group, and never admits
  `0.0.0.0/0` on the CIDR path.
- The generated connection string requires TLS (`sslmode=require`).

A regression that keeps the resource count but flips one of these (widens
the ingress, exposes the database, drops encryption) fails the gate.

### Live apply/destroy smoke test

Plan reaches no AWS API; a live apply confirms the resources actually
create and a client connects. Run it in a throwaway account or VPC, not
production. Requires AWS credentials and an existing VPC with two private
subnets in different availability zones.

```sh
cd install/terraform/mode3-postgres
cat > smoke.tfvars <<'EOF'
name                       = "sparkwing-smoke"
vpc_id                     = "vpc-REPLACE"
subnet_ids                 = ["subnet-REPLACE_A", "subnet-REPLACE_B"]
allowed_cidr_blocks        = ["10.0.0.0/16"]   # or your runner's SG
deletion_protection        = false
skip_final_snapshot        = true
EOF

terraform init
terraform apply -var-file=smoke.tfvars        # rds (default)
# expect: 6 resources created; outputs include endpoint + dsn_secret_arn

# Confirm a client connects via the emitted secret:
DSN="$(aws secretsmanager get-secret-value \
  --secret-id "$(terraform output -raw dsn_secret_name)" \
  --query SecretString --output text)"
psql "$DSN" -c 'select 1'                      # from inside the VPC

terraform destroy -var-file=smoke.tfvars
```

Repeat with `-var engine=aurora-serverless-v2` to smoke the Aurora path
(7 resources). `psql` must run from a host that can reach the VPC, since
the database is not publicly accessible.
