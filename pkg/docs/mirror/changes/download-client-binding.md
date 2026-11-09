### Security

- **controller (Breaking):** Bind public CloudFront data and artifact download URLs to the requesting IPv4 address
  Fetch the URL from the same egress address that requested it. IPv6 signing
  requests must retry over IPv4, and the download distribution must disable IPv6.
