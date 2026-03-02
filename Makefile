.PHONY: docker
docker:
	DOCKER_BUILDKIT=1 docker build --progress=plain -f ./build/Dockerfile .

.PHONY: deploy
deploy:
	docker-compose -p schedule_agent -f ./depoly/compose.yaml up -d

.PHONY: inject
inject:
	wire gen ./inject

.PHONY: tidy
tidy:
	go mod tidy