# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

### Task Runner
This project uses Taskfile (task) for build automation:

```bash
# Install dependencies and setup development environment
task deps           # Download Go modules
task setup-dev      # Install linting tools and setup .env

# Development workflow
task dev            # Run in development mode with go run
task build          # Build for current platform
task build-linux    # Build for Linux (production deployment)
task fmt            # Format Go code
task lint           # Run golangci-lint checks
task test           # Run tests with coverage

# Docker commands
task docker-build   # Build Docker image
task docker-run     # Run Docker container
task docker-stop    # Stop and remove container

# Complete workflows
task all            # Clean, lint, test, and build
```

### Running the Application
```bash
# Development with auto-reload
task dev

# Specific environment configs
go run cmd/numind/main.go -c config_dev.yaml
go run cmd/numind/main.go -c config_prod.yaml
go run cmd/numind/main.go -c config_qa.yaml
```

### Testing and Quality
```bash
# Run all tests
task test

# Individual commands
go test -v ./...                    # Basic tests
go test -v -race ./...              # Race condition tests
go test -v -coverprofile=coverage.out ./...  # Coverage tests
go tool cover -html=coverage.out -o coverage.html  # HTML coverage report

# Linting
task lint
golangci-lint run ./...
```

## Architecture Overview

### Project Structure
- **cmd/numind/**: Application entry point
- **internal/numind/**: Core application logic
  - **biz/**: Business logic layer with domain-specific modules
  - **store/**: Data access layer with GORM
  - **controller/v1/**: HTTP handlers and API endpoints
- **pkg/**: Shared utilities and external packages
- **docs/**: Technical documentation
- **scripts/**: Deployment and utility scripts

### Core Architecture Pattern
This project follows a **Clean Architecture** pattern with three main layers:

1. **Controller Layer** (`internal/numind/controller/v1/`): HTTP handlers, request/response handling
2. **Business Layer** (`internal/numind/biz/`): Domain logic, business rules
3. **Store Layer** (`internal/numind/store/`): Data access, database operations

### Key Technologies
- **Web Framework**: Gin (HTTP router and middleware)
- **ORM**: GORM with MySQL driver
- **Authentication**: JWT tokens
- **Configuration**: Viper (YAML configs)
- **Logging**: Zap structured logging
- **Image Processing**: ChromeDP for headless browser rendering
- **Payment**: WeChat Pay integration
- **Real-time**: WebSocket support for chat features

### Business Modules
The application is organized into these main business domains:

- **User Management**: WeChat login, profiles, authentication
- **Image Processing**: Upload, AI processing, OCR
- **Card System**: AI-generated cards from images
- **Book System**: Collections of cards organized into books
- **Chat System**: AI chat with context awareness
- **Payment System**: WeChat Pay integration
- **Template System**: Card and book templates
- **Category System**: Organization and classification

### Key Components

#### Card Rendering System
Located in `internal/numind/biz/card/`, this is a complex system with multiple renderers:
- **ChromeDP Renderer**: Uses headless Chrome for high-quality HTML-to-image conversion
- **WebP Renderer**: Optimized WebP output
- **Dynamic Renderer**: Handles variable content layouts
- **Browser-free Renderer**: Fallback without browser dependencies

#### AI Integration
- **Ali (Alibaba Cloud)**: Text processing and generation
- **Baidu**: OCR and text analysis  
- **Volc (ByteDance)**: Additional AI services
- **WeChat**: Mini-program integration

#### Database Design
Multi-table design supporting the image → card → book workflow:
- Users authenticate via WeChat OpenID
- Images are processed through AI pipeline
- Cards are generated from processed images
- Books collect multiple cards with organization features

## Development Guidelines

### Configuration Management
- Environment-specific configs: `config_dev.yaml`, `config_prod.yaml`, `config_qa.yaml`
- Local overrides: `config_local.yaml` (gitignored)
- Use Viper for accessing config values: `viper.GetString("key")`

### Database Access Patterns
- Use the store pattern: `store.S` global instance
- Business logic in biz layer, not controllers
- GORM for ORM operations
- Transaction handling in business layer

### Error Handling
- Custom error types in `internal/pkg/errno/`
- Use structured logging with Zap
- HTTP responses via `core.WriteResponse()`

### Adding New Features
1. Create business logic in `internal/numind/biz/[module]/`
2. Add data access in `internal/numind/store/[module].go`
3. Create HTTP handlers in `internal/numind/controller/v1/[module]/`
4. Register routes in `internal/numind/router.go`
5. Add tests following existing patterns

### Image Processing Pipeline
When working with card rendering:
- Multiple renderer implementations exist in `biz/card/`
- Use coordinator pattern for renderer selection
- Handle both sync and async processing
- WebP optimization for performance
- Chrome headless rendering requires proper Docker setup

### Performance Considerations
- Use pagination for list endpoints
- Implement caching for frequently accessed data
- Optimize image processing with background jobs
- Monitor Chrome processes in production