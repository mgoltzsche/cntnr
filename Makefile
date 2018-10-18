BUILDIMAGE=local/cntnr-build:latest
LITEIDEIMAGE=local/cntnr-build:liteide
DOCKERRUN=docker run --name cntnr-build --rm -v "${REPODIR}:/work" -w /work -u `id -u`:`id -u`

REPODIR=$(shell pwd)
GOPATH=${REPODIR}/build
LITEIDE_WORKSPACE=${GOPATH}/liteide-workspace
PKGNAME=github.com/mgoltzsche/cntnr
PKGRELATIVEROOT=$(shell echo /src/${PKGNAME} | sed -E 's/\/+[^\/]*/..\//g')
VENDORLOCK=${REPODIR}/vendor/ready
BINARY=cntnr

# 'apparmor' tag cannot be used for runc yet since package is not yet available in alpine:3.7
BUILDTAGS_RUNC=seccomp selinux ambient
BUILDTAGS?=containers_image_ostree_stub containers_image_storage_stub containers_image_openpgp libdm_no_deferred_remove btrfs_noversion ${BUILDTAGS_RUNC}
BUILDTAGS_STATIC=${BUILDTAGS} linux static_build exclude_graphdriver_devicemapper mgoltzsche_cntnr_libcontainer
LDFLAGS_STATIC=${LDFLAGS} -extldflags '-static'

CNI_VERSION=0.6.0
CNIGOPATH=${GOPATH}/cni

COBRA=${GOPATH}/bin/cobra
PACKAGES:=$(shell go list $(BUILDFLAGS) . | grep -v github.com/mgoltzsche/cntnr/vendor)

export PATH := dist/bin:$(PATH)

all: binary-static cni-plugins-static

binary-static: .buildimage
	${DOCKERRUN} ${BUILDIMAGE} make binary BUILDTAGS="${BUILDTAGS_STATIC}" LDFLAGS="${LDFLAGS_STATIC}"

binary: dependencies
	# Building application:
	GOPATH="${GOPATH}" \
	go build -o dist/bin/${BINARY} -a -ldflags "${LDFLAGS}" -tags "${BUILDTAGS}" "${PKGNAME}"

generate: dependencies
	GOPATH="${GOPATH}" \
	go get github.com/golang/protobuf/protoc-gen-go
	# GOPATH="${GOPATH}"
	cd "${GOPATH}"/src/github.com/mgoltzsche/cntnr/vendor/github.com/rootless-containers/proto && \
	"${GOPATH}/bin/protoc-gen-go" --go_out=. rootlesscontainers.proto

test: dependencies
	# Run tests
	export GOPATH="${GOPATH}"; \
	#go test -tags "${BUILDTAGS}" -coverprofile "${GOPATH}/coverage.out" -cover `cd "${GOPATH}/src/${PKGNAME}" && go list -tags "${BUILDTAGS}" ./... | grep -Ev '/vendor/|^${PKGNAME}/build/'`
	export GOPATH="${GOPATH}"; cd "${GOPATH}/src/github.com/mgoltzsche/cntnr/image/builder" && go test -tags "${BUILDTAGS}" -run ImageBuilder

test-static: dependencies
	# Run tests using BUILDTAGS_STATIC
	export GOPATH="${GOPATH}"; \
	go test -tags "${BUILDTAGS_STATIC}" -coverprofile "${GOPATH}/coverage.out" -cover `cd "${GOPATH}/src/${PKGNAME}" && go list -tags "${BUILDTAGS_STATIC}" ./... | grep -Ev '/vendor/|^${PKGNAME}/build/'`

test-coverage: test
	GOPATH="${GOPATH}" go tool cover -html="${GOPATH}/coverage.out"

fmt:
	# Format the go code
	(find . -mindepth 1 -maxdepth 1 -type d; ls *.go) | grep -Ev '^(./vendor|./dist|./build|./.git)(/.*)?$$' | xargs -n1 gofmt -w

# TODO: Run lint per default if hints fixed
lint:
	export GOPATH="${GOPATH}"; \
	go get golang.org/x/lint/golint && \
	"${GOPATH}/bin/golint" $(shell export GOPATH="${GOPATH}"; cd "${GOPATH}/src/${PKGNAME}" && go list -tags "${BUILDTAGS_STATIC}" ./... 2>/dev/null | grep -Ev '/vendor/|^${PKGNAME}/build/')

runc: dependencies
	rm -rf "${GOPATH}/src/github.com/opencontainers/runc"
	mkdir -p "${GOPATH}/src/github.com/opencontainers"
	cp -r "${GOPATH}/src/${PKGNAME}/vendor/github.com/opencontainers/runc" "${GOPATH}/src/github.com/opencontainers/runc"
	ln -sr "${REPODIR}/vendor" "${GOPATH}/src/github.com/opencontainers/runc/vendor"
	cd "${GOPATH}/src/github.com/opencontainers/runc" && \
	export GOPATH="${GOPATH}" && \
	make clean && \
	make BUILDTAGS="${BUILDTAGS_RUNC}" && \
	cp runc "${REPODIR}/dist/bin/runc"

cni-plugins-static: .buildimage
	${DOCKERRUN} ${BUILDIMAGE} make cni-plugins LDFLAGS="${LDFLAGS_STATIC}"

cni-plugins:
	# Build CNI plugins
	mkdir -p "${CNIGOPATH}"
	wget -O "${CNIGOPATH}/cni-${CNI_VERSION}.tar.gz" "https://github.com/containernetworking/cni/archive/v${CNI_VERSION}.tar.gz"
	wget -O "${CNIGOPATH}/cni-plugins-${CNI_VERSION}.tar.gz" "https://github.com/containernetworking/plugins/archive/v${CNI_VERSION}.tar.gz"
	tar -xzf "${CNIGOPATH}/cni-${CNI_VERSION}.tar.gz" -C "${CNIGOPATH}"
	tar -xzf "${CNIGOPATH}/cni-plugins-${CNI_VERSION}.tar.gz" -C "${CNIGOPATH}"
	rm -rf "${CNIGOPATH}/src/github.com/containernetworking"
	mkdir -p "${CNIGOPATH}/src/github.com/containernetworking"
	mv "${CNIGOPATH}/cni-${CNI_VERSION}"     "${CNIGOPATH}/src/github.com/containernetworking/cni"
	mv "${CNIGOPATH}/plugins-${CNI_VERSION}" "${CNIGOPATH}/src/github.com/containernetworking/plugins"
	export GOPATH="${CNIGOPATH}" && \
	for TYPE in main ipam meta; do \
		for CNIPLUGIN in `ls ${CNIGOPATH}/src/github.com/containernetworking/plugins/plugins/$$TYPE`; do \
			(set -x; go build -o dist/cni-plugins/$$CNIPLUGIN -a -ldflags "${LDFLAGS}" github.com/containernetworking/plugins/plugins/$$TYPE/$$CNIPLUGIN) || exit 1; \
		done \
	done

.buildimage:
	# Building build image:
	docker build -t ${BUILDIMAGE} --target cntnr-build .

build-sh: .buildimage
	# Running dockerized interactive build shell
	${DOCKERRUN} -ti ${BUILDIMAGE} /bin/sh

dependencies: .workspace
ifeq ($(shell [ ! -d vendor -o "${UPDATE_DEPENDENCIES}" = TRUE ] && echo 0),0)
	# Fetching dependencies:
	GOPATH="${GOPATH}" go get github.com/LK4D4/vndr
	rm -rf "${GOPATH}/vndrtmp"
	mkdir "${GOPATH}/vndrtmp"
	ln -sf "${REPODIR}/vendor.conf" "${GOPATH}/vndrtmp/vendor.conf"
	(cd build/vndrtmp && "${GOPATH}/bin/vndr" -whitelist='.*')
	rm -rf vendor
	mv "${GOPATH}/vndrtmp/vendor" vendor
else
	# Skipping dependency update
endif

update-dependencies:
	# Update dependencies
	@make dependencies UPDATE_DEPENDENCIES=TRUE
	# In case LiteIDE is running it must be restarted to apply the changes

.workspace:
	# Preparing build directory:
	[ -d "${GOPATH}" ] || \
		(mkdir -p vendor "$(shell dirname "${GOPATH}/src/${PKGNAME}")" \
		&& ln -sf "${PKGRELATIVEROOT}" "${GOPATH}/src/${PKGNAME}")

cobra: .workspace
	# Build cobra CLI to manage the application's CLI
	GOPATH="${GOPATH}" go get github.com/spf13/cobra/cobra
	"${GOPATH}/bin/cobra"

proot:
	cntnr image create --verbose --dockerfile Dockerfile --target proot --tag local/proot
	cntnr bundle create -b "${GOPATH}/proot-bundle" --update local/proot
	cp "${GOPATH}/proot-bundle/rootfs/proot" "${REPODIR}/dist/bin/proot"

liteide: dependencies
	rm -rf "${LITEIDE_WORKSPACE}"
	mkdir "${LITEIDE_WORKSPACE}"
	cp -r vendor "${LITEIDE_WORKSPACE}/src"
	mkdir -p "${LITEIDE_WORKSPACE}/src/${PKGNAME}"
	ln -sr "${REPODIR}"/* "${LITEIDE_WORKSPACE}/src/${PKGNAME}"
	(cd "${LITEIDE_WORKSPACE}/src/${PKGNAME}" && rm build vendor dist)
	GOPATH="${LITEIDE_WORKSPACE}" \
	BUILDFLAGS="-tags \"${BUILDTAGS}\"" \
	liteide "${LITEIDE_WORKSPACE}/src/${PKGNAME}" &
	################################################################
	# Setup LiteIDE project using the main package's context menu: #
	#  - 'Build Path Configuration':                               #
	#    - Make sure 'Inherit System GOPATH' is checked!           #
	#    - Configure BUILDFLAGS variable printed above             #
	#  - 'Lock Build Path' to the top-level directory              #
	#                                                              #
	# CREATE NEW TOP LEVEL PACKAGES IN THE REPOSITORY DIRECTORY    #
	# EXTERNALLY AND RESTART LiteIDE WITH THIS COMMAND!            #
	################################################################

ide: .liteideimage
	# Make sure to lock the build path to the top-level directory
	cntnr bundle create -b cntnr-liteide --update=true -w /work \
		--mount "src=${REPODIR},dst=/work/src/github.com/mgoltzsche/cntnr" \
		--mount "src=${REPODIR}/liteide.ini,dst=/root/.config/liteide/liteide.ini" \
		--mount src=/etc/machine-id,dst=/etc/machine-id,opt=ro \
		--mount src=/tmp/.X11-unix,dst=/tmp/.X11-unix \
		--env DISPLAY=$$DISPLAY \
		--env GOPATH=/work \
		${LITEIDEIMAGE} \
		liteide /work/src/github.com/mgoltzsche/cntnr
	cntnr bundle run --verbose cntnr-liteide &

.liteideimage:
	cntnr image create --dockerfile=Dockerfile --target=liteide --tag=${LITEIDEIMAGE}

LITEIDE_PKGS=g++ qt5-qttools qt5-qtbase-dev qt5-qtbase-x11 qt5-qtwebkit xkeyboard-config libcanberra-gtk3 adwaita-icon-theme ttf-dejavu
.OLD_liteideimage: .buildimage
	# TODO: clean this up when --workdir and --env options are supported
	cntnr image create \
		--from=docker-daemon:${BUILDIMAGE} \
		--author='Max Goltzsche <max.goltzsche@gmail.com>' \
		--run-sh='cd / && git clone https://github.com/visualfc/liteide.git \
			&& apk add --update --no-cache ${LITEIDE_PKGS} || /usr/lib/qt5/bin/qmake -help >/dev/null' \
		--run-sh='cd /liteide/build && ./update_pkg.sh \
			&& cd /liteide/build && QTDIR=/usr/lib/qt5 ./build_linux.sh \
			&& rm -rf /usr/local/bin; ln -s /liteide/build/liteide/bin /usr/local/bin' \
		--tag=${LITEIDEIMAGE}

install:
	cp dist/bin/cntnr /usr/local/bin/cntnr

clean:
	rm -rf ./build ./dist
