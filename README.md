# PGSTY KES

This is the PGSTY fork of MinIO KES, used as an optional key encryption
service for [SILO](https://github.com/pgsty/silo). The upstream open-source
repository is deprecated; PGSTY build and release work is maintained here.

KES remains a separate `kes` executable. Its HTTP API, configuration and
Vault-compatible keystore integration are preserved.

<p align="center">
  <img src='.github/logo.svg?sanitize=true' width='55%'>
</p>

***

**KES is a cloud-native distributed key management and encryption server designed to secure modern applications at scale.**

 - [What is KES?](#what-is-kes)
 - [Installation](#install)
 - [Quick Start](#quick-start)
 - [Documentation](#docs)
 
## What is KES?

KES (Key Encryption Service) is a distributed key management server that scales horizontally. It can either be run as edge server close to the applications
reducing latency to and load on a central key management system (KMS) or as central key management service. KES nodes are self-contained
stateless instances that can be scaled up and down automatically.

<p align="center">
  <img src='.github/arch.png?sanitize=true' width='70%'>
</p>

## Install

PGSTY release artifacts are published at
[pgsty/kes releases](https://github.com/pgsty/kes/releases). Until the first
PGSTY release is published, build this checkout:

```sh
git clone https://github.com/pgsty/kes.git
cd kes
go build -trimpath -o kes ./cmd/kes
./kes --version
```

The release pipeline builds Linux, macOS and Windows archives for AMD64 and
ARM64, plus Linux RPM, DEB and APK packages. Container publication targets
`docker.io/pgsty/kes` and is a separate, manually invoked step. An archive or
workflow configuration is not evidence that a release has been published.

`kes update` is disabled in this fork. Upgrade through packages, images or
verified PGSTY release artifacts; it must never download an upstream binary.
See [RELEASING.md](RELEASING.md) for build, verification and publication steps.

## Quick Start
   
We run a public KES instance at `https://play.min.io:7373` as playground.
You can interact with our play instance either via the KES CLI or cURL.
Alternatively, you can get started by setting up your own KES server in
less than five minutes.
   
<details><summary><b>First steps</b></summary>

#### 1. Configure CLI
Point the KES CLI to the KES server at `https://play.min.io:7373` and use the following API key:
```sh
export KES_SERVER=https://play.min.io:7373
export KES_API_KEY=kes:v1:AD9E7FSYWrMD+VjhI6q545cYT9YOyFxZb7UnjEepYDRc
```

#### 3. Create a Key
Create a new root encryption key - e.g. `my-key`.
```
kes key create my-key
```
> Note that creating a new key will fail with `key already exist` if it already exist.

#### 4. Generate a DEK
Derive a new data encryption keys (DEK).
```sh
kes key dek my-key
```
The plaintext part of the DEK would be used by an application to encrypt some data.
The ciphertext part of the DEK would be stored alongside the encrypted data for future
decryption.

</details>   

## Docs

If you want to learn more about KES checkout our [documentation](https://min.io/docs/kes/).
 - [Integration Guides](https://github.com/minio/kes/wiki#supported-kms-targets)
 - [Command Line](https://min.io/docs/kes/cli/#available-commands)
 - [Server API](https://min.io/docs/kes/concepts/server-api/)
 - [Go SDK](https://pkg.go.dev/github.com/minio/kes-go)

### Monitoring

KES servers provide an API endpoint `/v1/metrics` that observability tools, like [Prometheus](https://prometheus.io/), can scrape.  
Refer to the [monitoring documentation](https://min.io/docs/kes/concepts/monitoring/) for how to setup and capture KES metrics.

For a graphical Grafana dashboard refer to the following [example](examples/grafana/dashboard.json).

![](.github/grafana-dashboard.png)  

## FAQs

<details><summary><b>I have received an <code>insufficient permissions</code> error</b></summary>
   
This means that you are using a KES identity that is not allowed to perform a specific operation, like creating or listing keys.

The KES [admin identity](https://github.com/minio/kes/blob/6452cdc079dfae54e4a46102cb4622c80b99776f/server-config.yaml#L8)
can perform any general purpose API operation. You should never experience a `not authorized: insufficient permissions`
error when performing general purpose API operations using the admin identity.

In addition to the admin identity, KES supports a [policy-based](https://github.com/minio/kes/blob/6452cdc079dfae54e4a46102cb4622c80b99776f/server-config.yaml#L77) access control model.
You will receive a `not authorized: insufficient permissions` error in the following two cases:
1. **You are using a KES identity that is not assigned to any policy. KES rejects requests issued by unknown identities.**
   
   This can be fixed by assigning a policy to the identity. Checkout the [examples](https://github.com/minio/kes/blob/6452cdc079dfae54e4a46102cb4622c80b99776f/server-config.yaml#L79-L88).
2. **You are using a KES identity that is assigned to a policy but the policy either not allows or even denies the API call.**
   
   In this case, you have to grant the API permission in the policy assigned to the identity. Checkout the [list of APIs](https://github.com/minio/kes/wiki/Server-API#api-overview).
   For example, when you want to create a key you should allow the `/v1/key/create/<key-name>`. The `<key-name>` can either be a
   specific key name, like `my-key-1` or a pattern allowing arbitrary key names, like `my-key*`.
   
   Also note that deny rules take precedence over allow rules. Hence, you have to make sure that any deny pattern does not
   accidentally matches your API request.

</details>   
   
***

## License
Use of `KES` is governed by the AGPLv3 license that can be found in the [LICENSE](./LICENSE) file.
