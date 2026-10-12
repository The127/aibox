# The kernel of the VM image for arm64, which "just image" on macOS and
# "just image-docker-arm64" build before the Containerfile. It follows
# kernel.Imagefile. The build puts the Debian version of the kernel source in
# the kernel's version string, where the release workflow reads it to name
# its cache.

# Each kernel of the image starts from this stage, so that the source is
# unpacked once.
FROM debian:sid AS source

RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential bc bison flex libelf-dev libssl-dev cpio xz-utils linux-source-7.2
RUN mkdir /src && tar -xf /usr/src/linux-source-7.2.tar.xz -C /src && mv /src/linux-source-7.2 /src/linux
COPY check-kernel-config /src/

FROM source AS kernel

# vz-arm64.config is a complete kernel configuration for Virtualization.framework,
# the one of the kernel Apple's container tool boots on it, taken over as it
# is. kernel-arm64.config holds what aibox changes about it.
COPY vz-arm64.config kernel-arm64.config /src/
RUN cd /src/linux && \
    scripts/kconfig/merge_config.sh -m /src/vz-arm64.config /src/kernel-arm64.config && make olddefconfig
RUN sh /src/check-kernel-config /src/linux/.config /src/kernel-arm64.config
# Virtualization.framework boots the uncompressed Image of arm64
RUN cd /src/linux && make -j"$(nproc)" KBUILD_BUILD_VERSION="$(dpkg-query -W -f '${Version}' linux-source-7.2)" Image

FROM scratch AS out

COPY --from=kernel /src/linux/arch/arm64/boot/Image /vmlinuz
