# Copyright IBM Corp. 2017, 2026
# SPDX-License-Identifier: MPL-2.0

repository {
	go_modules = true
	osv        = true

	secrets {
		all = true
	}

	# Triage items that are _safe_ to ignore here. Note that this list should be
	# periodically cleaned up to remove items that are no longer found by the scanner.
	triage {
		suppress {
			vulnerabilities = [
				# busybox@1.37.0-r12 https://nvd.nist.gov/vuln/detail/CVE-2025-46394
				#
				# ESM does not shell out to the busybox tar program.
				"CVE-2025-46394",

				# busybox@1.37.0-r12 https://nvd.nist.gov/vuln/detail/CVE-2024-58251
				#
				# ESM does not shell out to the busybox netstat program.
				"CVE-2024-58251",
				
				# golang.org/x/crypto/openpgp package is not used by ESM.
				# Also this openpgp package is no longer maintained.
				# Suppressing as it is not relevant to ESM.
				"GO-2026-5932",
			]
		}
	}
}

container {
	dependencies    = true
	alpine_security = true
	osv             = true
	go_modules      = true
	secrets         = true

	# Triage items that are _safe_ to ignore here. Note that this list should be
	# periodically cleaned up to remove items that are no longer found by the scanner.
	triage {
		suppress {
			vulnerabilities = [
				# busybox@1.37.0-r12 https://nvd.nist.gov/vuln/detail/CVE-2025-46394
				#
				# ESM does not shell out to the busybox tar program.
				"CVE-2025-46394",

				# busybox@1.37.0-r12 https://nvd.nist.gov/vuln/detail/CVE-2024-58251
				#
				# ESM does not shell out to the busybox netstat program.
				"CVE-2024-58251",
				
				# golang.org/x/crypto/openpgp package is not used by ESM.
				# Also this openpgp package is no longer maintained.
				# Suppressing as it is not relevant to ESM.
				"GO-2026-5932",
			]
		}
	}
}

binary {
	secrets      = true
	go_modules   = true
	osv          = true
	oss_index    = false
	nvd          = false

	triage {
		suppress {
			vulnerabilities = [
				"GO-2022-0635",
				"GO-2025-3408",
				# golang.org/x/crypto/openpgp package is not used by ESM.
				# Also this openpgp package is no longer maintained.
				# Suppressing as it is not relevant to ESM.
				"GO-2026-5932",

				# github.com/hashicorp/consul is pinned to a consul main-branch commit
				# (2.1.0-dev, v1.11.0-alpha.0.20260922143634-a221db5d588a), the same one used
				# by consul-k8s. Its Go pseudo-version has a v1.11.0-alpha.0 base, so the
				# scanner treats it as older than the releases that fixed these advisories.
				# Remove once ESM moves to a tagged consul release.
				"GHSA-5c4w-8hhh-3c3h",
				"GHSA-chgm-7r52-whjj",
				"GHSA-m69r-9g56-7mv8",
				"GHSA-cpfq-66p2-336j",
				"GHSA-7g3r-8c6v-hfmr",
				"GO-2024-2683",
				"GO-2022-1029",
				"GHSA-qh7p-pfq3-677h",
				"GHSA-hr3v-8cp3-68rf",
				"GHSA-c57c-7hrj-6q6v",
				"GO-2025-4081",
				"GHSA-99wr-c2px-grmh",
				"GO-2024-3243",
				"GO-2025-4082",
				"GO-2023-1827",
				"GO-2024-3241",
				"GO-2024-3242",
				"GO-2026-4690",
			]
		}
	}
}