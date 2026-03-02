.PHONY: docker
docker:
	DOCKER_BUILDKIT=1 docker build --progress=plain -f ./build/Dockerfile .

.PHONY: deploy
deploy:
	docker-compose -p demo -f ./depoly/compose.yaml up -d --build

.PHONY: inject
inject:
	wire gen ./inject

.PHONY: tidy
tidy:
	go mod tidy
