# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

repository {
	go_modules = true
	osv        = true

	secrets {
		all = true
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
			vulnerabilites = [
				"GO-2022-0635",
				"GO-2025-3408",
				# golang.org/x/crypto/openpgp package is not used by ESM.
				# Also this openpgp package is no longer maintained.
				# Suppressing as it is not relevant to ESM.
				"GO-2026-5932",
			]
		}
	}
}