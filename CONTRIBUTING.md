# Contributing to SyncerD

Thank you for your interest in contributing to SyncerD! This document provides guidelines and instructions for contributing.

## Code of Conduct

This project adheres to a Code of Conduct that all contributors are expected to follow. Please be respectful and constructive in all interactions.

## How to Contribute

### Reporting Bugs

1. Check if the bug has already been reported in [Issues](https://github.com/clouddrove/syncerd/issues)
2. If not, create a new issue with:
   - A clear, descriptive title
   - Steps to reproduce the bug
   - Expected vs actual behavior
   - Environment details (OS, Go version, etc.)
   - Relevant logs or error messages

### Suggesting Features

1. Check if the feature has already been suggested
2. Open an issue with:
   - A clear description of the feature
   - Use cases and benefits
   - Potential implementation approach (if you have ideas)

### Pull Requests

1. **Fork the repository**
2. **Create a feature branch**: `git checkout -b feature/your-feature-name`
3. **Make your changes**:
   - Follow Go coding standards
   - Add tests for new functionality
   - Update documentation as needed
   - Ensure all tests pass
4. **Commit your changes**: Use clear, descriptive commit messages
5. **Push to your fork**: `git push origin feature/your-feature-name`
6. **Open a Pull Request**:
   - Provide a clear description of changes
   - Reference any related issues
   - Ensure CI checks pass

## Development Setup

1. Clone your fork:
   ```bash
   git clone https://github.com/your-username/syncerd.git
   cd syncerd
   ```

2. Install dependencies:
   ```bash
   go mod download
   ```

3. Build the project:
   ```bash
   make build
   ```

4. Run tests:
   ```bash
   make test
   ```

## Coding Standards

- Follow Go conventions and best practices
- Use `gofmt` to format code
- Add comments for exported functions and types
- Keep functions focused and small
- Handle errors explicitly (don't ignore them)
- Write tests for new functionality

## Testing

- Write unit tests for new features
- Ensure all existing tests pass
- Test with different registry configurations
- Test error handling paths

## Documentation

- Update README.md for user-facing changes
- Add code comments for complex logic
- Update example configurations if needed
- Keep CHANGELOG.md updated (if applicable)

## Questions?

Feel free to open an issue for any questions or clarifications. We're happy to help!
