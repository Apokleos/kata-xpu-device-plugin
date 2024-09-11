DOCKER_REPO ?= kata-xpu-device-plugin
DOCKER_TAG ?= v1.3.1
ALIAS_REPO ?= docker.io/library

build:
	go build -buildvcs=false -o kata-xpu-device-plugin kata-xpu-device-plugin/cmd
clean:
	rm -rf kata-xpu-device-plugin && rm -rf coverage.out
clean-image:
	ctr -n k8s.io images rm $(ALIAS_REPO)/$(DOCKER_REPO):$(DOCKER_TAG)
	docker rmi $(DOCKER_REPO):$(DOCKER_TAG) && rm -rf $(DOCKER_REPO)-$(DOCKER_TAG).tar
build-image:
	docker build . -t $(DOCKER_REPO):$(DOCKER_TAG)
	docker save $(DOCKER_REPO):$(DOCKER_TAG) -o $(DOCKER_REPO)-$(DOCKER_TAG).tar
	ctr -n k8s.io image import $(DOCKER_REPO)-$(DOCKER_TAG).tar
	crictl images |grep $(DOCKER_REPO)
