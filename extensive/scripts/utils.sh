utils::get_architecture() {
    arch=$(uname -m)
    if [ "$arch" = "aarch64" ]; then
        echo "arm64"
    elif [ "$arch" = "x86_64" ]; then
        echo "amd64"
    else
        echo "unknown"
    fi
}

utils::fetch_yq() {
    local script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    # 检查是否已经有yq
    if [ -f "$script_dir/yq" ]; then
        echo "yq already exists in $script_dir"
        return
    fi
    local yq_version="v4.44.3"
    local arch=$(utils::get_architecture)
    echo "Fetching yq for $arch to $script_dir"
    local url="https://github.com/mikefarah/yq/releases/download/$yq_version/yq_linux_$arch"
    curl -L -o "$script_dir/yq" $url
    chmod +x "$script_dir/yq"
}

utils::fetch_yq