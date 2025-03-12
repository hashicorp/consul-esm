# Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

container {
	dependencies = true
	alpine_secdb = true
	secrets      = true
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
				"GO-2025-3408"
			]
		}
	}
}