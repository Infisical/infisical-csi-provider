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

## Bounding how long secrets stay readable

By default a mounted secret stays readable for the pod's whole life, which hands the same access to
anything that later achieves code execution inside it. Setting `windowDuration` serves the secrets
only while the containers are freshly started, which is when the application actually needs them.

```yaml
  parameters:
    windowDuration: "5m"
```

Once the window closes, the provider serves empty files and stops calling Infisical altogether: the
secret never leaves the platform, rather than being fetched and then withheld. The driver writes back
whatever payload the provider returns, which is what blanks the file. When a container restarts, the
window reopens, because a restarted container gets a fresh start time and needs its secrets again,
and the kubelet replays nothing on its behalf.

The window is evaluated from the pod's own status in the API server, which the pod cannot forge, so the
provider needs to read pods. Set `readWindow.enabled=true` on the chart to grant that and to scope the
watch to each node; the permission is not granted otherwise, since most installs will not use this.

Things this needs to work, and things it does not do:

- **Rotation must be enabled on the driver**, with `--enable-secret-rotation=true` and a short
  `--rotation-poll-interval`. The provider is otherwise only called at mount time and nothing ever
  closes. The file blanks up to one poll interval after the window ends, and refills up to one
  interval after a restart, so applications should retry their first read.
- **It is incompatible with `secretObjects`.** The driver syncs mounted contents into a
  namespace-scoped Kubernetes Secret independently of the window, so closing one pod's window writes
  empty values into a Secret that other workloads may read. A per-pod exposure window cannot be
  expressed through a cluster-wide object, and the provider cannot detect the conflict itself:
  `spec.secretObjects` is not part of `spec.parameters`, which is all it receives.
- **The window must be comfortably longer than it takes a grant to become usable.** Against a
  self-hosted instance, permission changes took about 11 seconds to take effect, which is why one
  minute is too tight and five is the suggested starting point.
- **A volume is the unit of enforcement, a container is the unit of decision.** There is one file for
  every container mounting the volume, so if two of them mount it, either one restarting refills the
  file for both, and a long-running container regains access when its neighbour crashes. A tight window
  wants exactly one consumer per volume.
- Nothing protects the secret *during* the window. Code running in the pod while it is open reads
  the file. What this removes is the standing ability to read it for hours or days afterwards.

Leaving `windowDuration` unset keeps the previous behaviour.

## AWS authentication

`authMethod: "aws"` logs in by signing an `sts:GetCallerIdentity` with the credentials the provider
already has on the node, rather than forwarding the pod's service account token.

```yaml
  parameters:
    authMethod: "aws"
    identityId: "<machine identity with AWS Auth configured>"
```

The provider needs AWS credentials of its own, and the machine identity's AWS Auth config has to trust
that principal. Prefer IRSA over the node instance profile, since it scopes the credential to this
provider rather than to everything on the node; set the role on `serviceAccount.annotations` in the
chart.

One tradeoff to weigh, because it is not a like-for-like swap. Under Kubernetes auth, Infisical
validates the *pod's* token, so an identity configured for one namespace and service account cannot be
used from another. Under AWS auth the provider signs with its own credentials, identical for every pod
on the node, and the pod contributes nothing to the decision: anyone who can create a
SecretProviderClass can name any machine identity whose AWS Auth config trusts that principal. Scope
those identities narrowly.

Kubernetes auth requires Infisical to validate the pod's token by calling TokenReview against the
cluster API server. That is not always possible: on a cluster whose API endpoint resolves to a private
address, Infisical refuses it outright with `Local IPs not allowed as URL`, which leaves the provider
unusable. AWS auth has no such requirement, since nothing has to reach back into the cluster.

Omitting `authMethod`, or setting it to `kubernetes`, keeps the previous behaviour.

## Troubleshooting

To troubleshoot issues with the Infisical CSI provider, refer to the logs of the Infisical CSI provider running on the same node as your pod.

  ```bash
  kubectl logs infisical-csi-provider-7x44t
  ```

You can also refer to the logs of the secrets store CSI driver. Modify the command below with the appropriate pod and namespace of your secrets store CSI driver installation.

  ```bash
  kubectl logs csi-secrets-store-csi-driver-7h4jp -n=kube-system
  ```
