# SES domain identity + Easy DKIM for the domain. DKIM-based verification also
# verifies the domain, so no separate TXT record is needed. The DKIM CNAMEs are
# published in dns.tf. Leaving the SES sandbox stays a manual request (README).

resource "aws_ses_domain_identity" "main" {
  domain = var.domain
}

resource "aws_ses_domain_dkim" "main" {
  domain = aws_ses_domain_identity.main.domain
}
