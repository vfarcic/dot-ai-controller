# Troubleshooting

Common issues and solutions.

## Pod Not Starting

Check the logs:

```bash
kubectl logs -l app=dot-ai-controller
```

## CRD Not Found

Ensure CRDs are installed:

```bash
kubectl get crd solutions.dot-ai.devopstoolkit.live
```
