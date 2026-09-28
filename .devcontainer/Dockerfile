FROM mcr.microsoft.com/devcontainers/go:1-bookworm

# Defining versions
ARG KIND_VERSION="v0.31.0"
ARG KUBEBUILDER_VERSION="v4.14.0"
ARG GINKGO_VERSION="v2.28.3"
ARG KUBECTL_VERSION="v1.36.0"

USER root

# Install packages
RUN curl -Lo ./kind https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64 \
    && chmod +x ./kind \
    && mv ./kind /usr/local/bin/kind

RUN curl -L -o kubebuilder "https://github.com/kubernetes-sigs/kubebuilder/releases/download/${KUBEBUILDER_VERSION}/kubebuilder_linux_amd64" \
    && chmod +x kubebuilder \
    && mv kubebuilder /usr/local/bin/

    # Install kubectl
RUN curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
    && chmod +x kubectl \
    && mv kubectl /usr/local/bin/

USER vscode

RUN go install github.com/onsi/ginkgo/v2/ginkgo@${GINKGO_VERSION}