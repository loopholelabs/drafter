drafter-packager --package-path image/drafteros.tar.zst --extract --devices '[
  {
    "name": "kernel",
    "path": "image/blueprint/vmlinux"
  },
  {
    "name": "disk",
    "path": "image/blueprint/rootfs.ext4"
  }
]'

drafter-packager --package-path image/oci-valkey.tar.zst --extract --devices '[
  {
    "name": "oci",
    "path": "image/blueprint/oci.ext4"
  }
]'
