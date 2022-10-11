

VERSION=$(date +%Y-%m-%dT%H.%M.%S)-$(git log -1 --pretty=format:"%h")
IMAGE_NAME=crholm/zdap-proxy

docker buildx build -f Dockerfile.zdap-proxy \
    --push \
    --platform=linux/amd64,linux/arm64 \
    -t ${IMAGE_NAME}:latest \
    -t ${IMAGE_NAME}:${VERSION} \
    . || exit 1

