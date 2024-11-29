# Infisical Provider for Secrets Store CSI Driver

Infisical provider for the [Secrets Store CSI driver](https://github.com/kubernetes-sigs/secrets-store-csi-driver) will allow you to mount Infisical secrets directly into your Kubernetes pods while maintaining secret-zero in your Kubernetes cluster.

## Installation

### Prerequisites

* Kubernetes version >= 1.20
* [Secrets store CSI driver](https://secrets-store-csi-driver.sigs.k8s.io/getting-started/installation.html) installed with `tokenRequests` audience configured
* Kubernetes service account configured for [native authentication](https://infisical.com/docs/documentation/platform/identities/kubernetes-auth) with Infisical

### Using helm (Recommended)
```bash
helm repo add infisical-helm-charts 'https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts' 
  
helm repo update

helm install infisical-csi-provider infisical-helm-charts/infisical-csi-provider
```

### Using yaml

You can also install using the deployment config in the `deployment` folder:

```bash
kubectl apply -f deployment/infisical-csi-provider.deployment.yaml
```

## Usage
For guidance, refer to the official documentation [here](https://infisical.com/docs/integrations/platforms/kubernetes-csi).

## Troubleshooting

To troubleshoot issues with the Infisical CSI provider, refer to the logs of the Infisical CSI provider running on the same node as your pod.

  ```bash
  kubectl logs infisical-csi-provider-7x44t
  ```

You can also refer to the logs of the secrets store CSI driver. Modify the command below with the appropriate pod and namespace of your secrets store CSI driver installation.

  ```bash
  kubectl logs csi-secrets-store-csi-driver-7h4jp -n=kube-system
  ```
