# WeCom Agent Service - SDK 文件放置说明

## 目录用途

此目录用于存放腾讯企业微信会话存档 C++ SDK 文件。

## 需要的文件

请从 [企业微信开发者中心](https://developer.work.weixin.qq.com/document/path/91774) 下载 Linux 版 SDK，并将以下文件放入此目录：

1. **libWeWorkFinanceSdk.so** - C++ 动态链接库
2. **WeWorkFinanceSdk_C.h** - C 语言头文件（如果有）

## 下载链接

- [Linux x86_64 SDK v3.0](https://wwcdn.weixin.qq.com/node/wwcomm/sdk_x86_v3_20250205.tgz)
- [Linux ARM64 SDK v3.0](https://wwcdn.weixin.qq.com/node/wwcomm/sdk_arm_v3_20250205.tgz)
- [Linux x86_64 SDK v2.0](https://wwcdn.weixin.qq.com/node/wework/images/sdk_20240606.tgz)

## 安装步骤

```bash
# 1. 下载 SDK
wget https://wwcdn.weixin.qq.com/node/wwcomm/sdk_x86_v3_20250205.tgz

# 2. 解压
tar -xzf sdk_x86_v3_20250205.tgz

# 3. 复制文件到此目录
cp libWeWorkFinanceSdk.so /path/to/numind-server/lib/wecom-sdk/
cp WeWorkFinanceSdk_C.h /path/to/numind-server/lib/wecom-sdk/  # 如果有
```

## 编译注意事项

如果在 macOS 上开发，你无法直接编译包含 CGO 的代码。请使用以下方式：

1. **Docker 构建**：使用 `Dockerfile.wecom-agent` 在 Linux 容器中编译
2. **交叉编译**：设置 `GOOS=linux GOARCH=amd64` 但需要 Linux 工具链
3. **服务器编译**：在目标服务器上直接编译

## 验证 SDK

在 Linux 环境下验证 SDK 是否可用：

```bash
# 检查动态库依赖
ldd libWeWorkFinanceSdk.so

# 应该看到类似输出：
# linux-vdso.so.1 => ...
# libpthread.so.0 => /lib/x86_64-linux-gnu/libpthread.so.0
# libssl.so.1.1 => /lib/x86_64-linux-gnu/libssl.so.1.1
# ...
```

如果有缺失的依赖，请安装：
```bash
apt-get install libssl-dev
```
