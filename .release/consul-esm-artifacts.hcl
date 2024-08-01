schema = 1
artifacts {
  zip = [
    "consul-esm_${version}_darwin_amd64.zip",
    "consul-esm_${version}_darwin_arm64.zip",
    "consul-esm_${version}_freebsd_386.zip",
    "consul-esm_${version}_freebsd_amd64.zip",
    "consul-esm_${version}_freebsd_arm.zip",
    "consul-esm_${version}_linux_386.zip",
    "consul-esm_${version}_linux_amd64.zip",
    "consul-esm_${version}_linux_arm.zip",
    "consul-esm_${version}_linux_arm64.zip",
    "consul-esm_${version}_netbsd_386.zip",
    "consul-esm_${version}_netbsd_amd64.zip",
    "consul-esm_${version}_netbsd_arm.zip",
    "consul-esm_${version}_openbsd_386.zip",
    "consul-esm_${version}_openbsd_amd64.zip",
    "consul-esm_${version}_openbsd_arm.zip",
    "consul-esm_${version}_solaris_amd64.zip",
    "consul-esm_${version}_windows_386.zip",
    "consul-esm_${version}_windows_amd64.zip",
  ]
  rpm = [
    "consul-esm-${version_linux}-1.aarch64.rpm",
    "consul-esm-${version_linux}-1.armv7hl.rpm",
    "consul-esm-${version_linux}-1.i386.rpm",
    "consul-esm-${version_linux}-1.x86_64.rpm",
  ]
  deb = [
    "consul-esm_${version_linux}-1_amd64.deb",
    "consul-esm_${version_linux}-1_arm64.deb",
    "consul-esm_${version_linux}-1_armhf.deb",
    "consul-esm_${version_linux}-1_i386.deb",
  ]
  container = [
    "consul-esm_release-default_linux_386_${version}_${commit_sha}.docker.dev.tar",
    "consul-esm_release-default_linux_386_${version}_${commit_sha}.docker.tar",
    "consul-esm_release-default_linux_amd64_${version}_${commit_sha}.docker.dev.tar",
    "consul-esm_release-default_linux_amd64_${version}_${commit_sha}.docker.tar",
    "consul-esm_release-default_linux_arm64_${version}_${commit_sha}.docker.dev.tar",
    "consul-esm_release-default_linux_arm64_${version}_${commit_sha}.docker.tar",
    "consul-esm_release-default_linux_arm_${version}_${commit_sha}.docker.dev.tar",
    "consul-esm_release-default_linux_arm_${version}_${commit_sha}.docker.tar",
  ]
}
