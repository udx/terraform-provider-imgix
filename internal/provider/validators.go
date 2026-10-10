package provider

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var (
	// Bare imgix subdomain: lowercase DNS label, no dots, no imgix.net suffix.
	subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	// GCS bucket name, explicitly rejecting a gs:// prefix.
	gcsBucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)

	// Full DNS hostname for custom domains (labels separated by dots).
	dnsNameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

	// Absolute http(s) URL for fallback images; empty string is the explicit
	// "clear" sentinel handled by the clearableString plan modifier.
	absoluteURLRe = regexp.MustCompile(`^(|https?://\S+)$`)
)

func subdomainValidator() validator.String {
	return stringvalidator.RegexMatches(subdomainRe,
		"must be a bare lowercase subdomain such as \"example-media\" — not a full hostname (no dots, no .imgix.net suffix)")
}

func gcsBucketValidator() validator.String {
	return stringvalidator.RegexMatches(gcsBucketRe,
		"must be a GCS bucket name without a gs:// prefix")
}

func dnsNameValidator() validator.String {
	return stringvalidator.RegexMatches(dnsNameRe,
		"must be a full DNS hostname such as \"media.example.com\"")
}

func absoluteURLValidator() validator.String {
	return stringvalidator.RegexMatches(absoluteURLRe,
		"must be an absolute URL starting with http:// or https://")
}
