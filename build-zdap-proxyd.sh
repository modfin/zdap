

VERSION=$(date +%Y-%m-%dT%H.%M.%S)-$(git log -1 --pretty=format:"%h")
IMAGE_NAME=crholm/zdap-proxy

docker build -f Dockerfile.zdap-proxy \
    -t ${IMAGE_NAME}:latest \
    -t ${IMAGE_NAME}:${VERSION} \
    . || exit 1


docker push ${IMAGE_NAME}:${VERSION}
docker push ${IMAGE_NAME}:latest

