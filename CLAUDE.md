# Claude Code Configuration for PROJECT_NAME

## Project Overview
This is a Go project with modern development practices and Claude Code integration.

## Code Style Guidelines
- Follow standard Go conventions and idioms
- Use meaningful variable and function names
- Add comments for complex logic
- Maintain consistent error handling patterns
- Write tests for all new functions

## Architecture Principles
- Keep packages focused and single-purpose
- Use interfaces for dependency injection
- Implement clean architecture (handlers → services → repositories)
- Separate business logic from infrastructure

## Development Workflow
1. Always run tests before committing
2. Use conventional commits (feat:, fix:, docs:, etc.)
3. Create feature branches from main
4. Write tests first (TDD) when possible
5. Run linters before pushing

## Go-Specific Guidelines
- Use `errors.Is()` and `errors.As()` for error checking
- Prefer `context.Context` for cancellation and timeouts
- Use `defer` for cleanup operations
- Implement proper graceful shutdown for servers
- Use structured logging (log/slog or similar)

## Testing Requirements
- Unit tests: minimum 80% coverage
- Use table-driven tests where appropriate
- Mock external dependencies
- Include integration tests for APIs
- Test error cases, not just happy paths

## Security Practices
- Never commit secrets or credentials
- Use environment variables for configuration
- Validate all user inputs
- Use prepared statements for SQL
- Implement rate limiting for APIs

## Performance Considerations
- Profile before optimizing
- Use connection pooling for databases
- Implement caching where appropriate
- Use goroutines judiciously
- Avoid premature optimization

## Git Workflow
- Branch naming: feature/*, bugfix/*, hotfix/*
- Commit message format: <type>(<scope>): <subject>
- PR description should include what, why, and how
- All PRs require tests and passing CI

## Available Commands
- `/test` - Run all tests with coverage
- `/lint` - Run all linters
- `/build` - Build the application
- `/deploy` - Deploy to staging/production
- `/spec-create` - Create a new feature specification

## Project-Specific Context
[Add your project-specific information here]
- Main entry point: cmd/main.go
- API documentation: docs/api.md
- Environment setup: See README.md

## Communication Style
- Explain complex changes before implementing
- Ask for clarification when requirements are unclear
- Provide progress updates for long-running tasks
- Suggest improvements when you see opportunities

## DO NOT
- Delete files without asking
- Make breaking API changes without discussion
- Remove tests or reduce coverage
- Commit directly to main branch
- Use deprecated Go features
