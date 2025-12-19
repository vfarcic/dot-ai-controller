# DevOps AI Toolkit Controller

A Kubernetes controller that provides resource tracking, event-driven remediation, and resource visibility capabilities.

<p align="center">
  <a href="https://devopstoolkit.ai/docs/controller/"><strong>Read the Documentation</strong></a>
</p>

## Overview

The DevOps AI Toolkit Controller bridges the gap between Kubernetes resources and intelligent operations through three CRDs:

- **Solution** - Track and manage deployed resources as logical solutions with automatic cleanup via ownerReferences
- **RemediationPolicy** - Monitor Kubernetes events and automatically remediate issues using AI-powered analysis
- **ResourceSyncConfig** - Enable semantic search and resource discovery across your cluster

<p align="center">
  <a href="https://devopstoolkit.ai/docs/controller/"><strong>Read the Documentation</strong></a>
</p>

## Support

- **Issues**: [GitHub Issues](https://github.com/vfarcic/dot-ai-controller/issues)
- **Discussions**: [GitHub Discussions](https://github.com/vfarcic/dot-ai-controller/discussions)

## Contributing & Governance

Contributions are welcome! Please feel free to submit a Pull Request.

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Acknowledgments

Built with [Kubebuilder](https://kubebuilder.io/) and designed to integrate with the [DevOps AI Toolkit](https://github.com/vfarcic/dot-ai).
