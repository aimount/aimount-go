package toolworker

import "regexp"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`awi_tst_[A-Za-z0-9_-]+`),
	regexp.MustCompile(`awi_tex_[A-Za-z0-9_-]+`),
	regexp.MustCompile(`awi_tco_[A-Za-z0-9_-]+`),
	regexp.MustCompile(`sha256:[A-Za-z0-9_-]+`),
}

func Redact(input string) string {
	output := input
	for _, pattern := range secretPatterns {
		output = pattern.ReplaceAllString(output, "[redacted]")
	}
	return output
}
