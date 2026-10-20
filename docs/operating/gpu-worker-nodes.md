---
title: "GPU Worker Nodes"
description: "Give Kubernetes worker nodes NVIDIA GPUs, on bare metal or through VM passthrough, so a Kubemoot ModelServer can use them."
weight: 4
---

Kubemoot schedules models onto GPUs, but it does not create GPU nodes. This page covers
the general path for making an NVIDIA GPU usable by Kubernetes workloads, whether the
worker node is a bare-metal machine or a virtual machine, and ends with how a Kubemoot
`ModelServer` then uses the node. A short worked example closes the page.

The end state is one check: the node reports `nvidia.com/gpu` capacity.

## Choose a path

| Your worker node is | GPU access | Start at |
|---------------------|------------|----------|
| A bare-metal machine with the GPU installed | Direct | [Install the driver and container toolkit](#install-the-driver-and-container-toolkit) |
| A VM on a hypervisor (Proxmox, KVM, ESXi, and similar) | PCI passthrough of the whole card | [Pass the GPU through to the VM](#pass-the-gpu-through-to-the-vm) |
| A VM on a cloud provider | The provider's GPU instance types | The provider's documentation, then [Install the driver and container toolkit](#install-the-driver-and-container-toolkit) |

Passthrough hands the entire physical GPU to one VM. The host loses access to it while
the VM runs, and no second VM can share it. Plan one GPU per GPU worker VM. A cluster
scales by adding such nodes.

## Pass the GPU through to the VM

Skip this section on bare metal.

### Enable IOMMU on the host

The hypervisor host needs hardware virtualization and an IOMMU:

1. In the firmware settings, enable the virtualization extensions (Intel VT-x and VT-d,
   or AMD-V and AMD-Vi) and, where the board offers it, "Above 4G Decoding" and
   "Resizable BAR" support. Large-VRAM cards need the 64-bit address space.
2. In the host kernel command line, turn the IOMMU on (`intel_iommu=on` for Intel; AMD
   enables it by default on current kernels) and add `iommu=pt` to avoid overhead for
   devices that are not passed through.
3. Reboot, then confirm the IOMMU is active:

```bash
dmesg | grep -i -e DMAR -e IOMMU
```

### Bind the GPU to VFIO

The host must not load its own NVIDIA or `nouveau` driver for the card. Bind the GPU and
its companion audio function to the `vfio-pci` driver instead.

```bash
# Find the vendor:device IDs of the GPU and its audio function
lspci -nn | grep -i nvidia

# Reserve them for vfio-pci (replace the IDs with the ones above)
echo "options vfio-pci ids=10de:xxxx,10de:yyyy" > /etc/modprobe.d/vfio.conf
echo "blacklist nouveau" > /etc/modprobe.d/blacklist-gpu.conf
update-initramfs -u   # or your distribution's equivalent
```

After a reboot, `lspci -nnk` shows `Kernel driver in use: vfio-pci` for both functions.

Check the IOMMU group of the card: the GPU and its audio function should sit in a group
with no unrelated devices. Devices that share a group must be passed through together.

### Attach the device to the VM

Add the GPU to the VM as a PCI device (on Proxmox, a `hostpci` entry). Use these VM
settings:

- **Machine type `q35`** and **UEFI firmware (OVMF)**, so the guest sees a PCIe topology.
- **PCIe passthrough mode** for the device, with all functions of the card.
- **A CPU type that exposes the host features** (`host`), which the NVIDIA driver expects.

### Consumer card quirks

Consumer GeForce cards were not designed for passthrough, and a few behaviors recur:

- **Reset on VM restart.** Some cards do not reset cleanly when the VM stops, and the next
  VM start fails or the guest driver cannot initialize the card. A full host power cycle
  recovers it. Plan GPU VM restarts accordingly, and prefer stopping and starting the VM
  over repeated reboots.
- **Exclusive ownership.** If the same GPU also backs another VM (a Windows machine, for
  example), only one of the two can run at a time. Stop one before starting the other,
  and drain the Kubernetes node first so workloads move off cleanly.
- **Empty VRAM after power events.** VRAM does not survive a host power cycle. Models
  load again on demand; Kubemoot does this on its own.
- **Driver choice.** Recent consumer GPU generations require NVIDIA's open-source kernel
  modules rather than the proprietary ones. Check the generation of your card against
  the driver documentation of your node operating system.

## Install the driver and container toolkit

Inside the guest (or on the bare-metal host), the node needs two things before
Kubernetes sees the GPU:

1. **The NVIDIA kernel driver**, loaded at boot. Confirm with `nvidia-smi` on a general
   purpose distribution.
2. **The NVIDIA container toolkit**, which teaches the container runtime
   (containerd) how to inject GPU devices into containers.

How you get them depends on the node operating system:

- **A general purpose distribution** (Ubuntu, Debian, and similar): install the driver
  package from the distribution or from NVIDIA, install the NVIDIA container toolkit,
  and configure containerd to use it. Reboot and confirm `nvidia-smi` lists the card.
- **An immutable node operating system** (Talos): there is no package manager. The driver
  and toolkit come as system extensions built into the node image. See the
  [worked example](#worked-example-proxmox-and-talos) below.

## Advertise the GPU to Kubernetes

Kubernetes learns about the GPU through the NVIDIA device plugin. There are two ways to
run it:

- **The NVIDIA GPU Operator**, a Helm chart that deploys the device plugin, GPU feature
  discovery (node labels such as GPU product and memory), and a validator. It can also
  install the driver and toolkit itself. When the node image already provides them
  (the Talos case, or a pre-built image), disable the operator's driver and toolkit
  components and let it manage only the plugin, discovery, and validation. This is the
  recommended path for current clusters.
- **The standalone NVIDIA device plugin** Helm chart, for clusters where the driver and
  toolkit are fully managed outside Kubernetes and you want the smallest footprint.

The GPU Operator runs privileged daemon sets. Label its namespace for the privileged Pod
Security level:

```bash
kubectl label namespace gpu-operator pod-security.kubernetes.io/enforce=privileged
```

Optionally taint GPU nodes (`nvidia.com/gpu=present:NoSchedule`) so only workloads that
tolerate the taint and request a GPU land there. A model server you pin to the node adds
the matching toleration.

## Check the result

```bash
# The node advertises GPU capacity
kubectl describe node <gpu-node> | grep nvidia.com/gpu

# The device plugin pod is running on the node
kubectl get pods -A -o wide | grep -e device-plugin -e gpu-operator
```

Expect `nvidia.com/gpu: 1` under both Capacity and Allocatable for a node with one
card. Then confirm a container can use it:

```bash
kubectl run gpu-test --rm -it --restart=Never \
  --image=nvidia/cuda:12.6.0-base-ubuntu22.04 \
  --overrides='{"spec":{"containers":[{"name":"gpu-test","image":"nvidia/cuda:12.6.0-base-ubuntu22.04","command":["nvidia-smi"],"resources":{"limits":{"nvidia.com/gpu":"1"}}}]}}'
```

Add a `nodeSelector` and a toleration to the overrides if you tainted the node. The
output is the `nvidia-smi` table listing your card.

If `nvidia.com/gpu` is missing, work from the bottom up:

| Symptom | Check |
|---------|-------|
| VM does not start, or the card is absent in the guest | IOMMU enabled, card bound to `vfio-pci`, IOMMU group clean, VM uses `q35` and OVMF |
| Guest does not list the card in `lspci` | The passthrough entry includes every function of the card |
| `nvidia-smi` fails in the guest | Driver loaded; driver generation matches the card |
| Device plugin errors on discovery | Container runtime configured for the NVIDIA runtime or CDI, depending on the node operating system |
| Node ready but no `nvidia.com/gpu` capacity | Device plugin pod running on that node; node not excluded by the plugin's node selector |

## How Kubemoot uses the node

Once the node has `nvidia.com/gpu` capacity, the rest follows the pattern in
[Put a model server on your GPU nodes](../../introduction/installation/#put-a-model-server-on-your-gpu-nodes):

1. Deploy a model server pinned to the GPU node, requesting `nvidia.com/gpu: 1`. vLLM,
   installed with the `kubemoot-vllm` chart, is the currently preferred engine
   ([Run vLLM on a GPU node](#run-vllm-on-a-gpu-node)); Ollama is the supported
   alternative ([Run Ollama on a GPU node](#run-ollama-on-a-gpu-node)).
2. Declare one `ModelServer` pointing at that server's Service.
3. The operator discovers the models, the node, and, when DCGM metrics are available,
   the GPU model and VRAM from the node. Every chat model the server has joins the
   cluster's model catalog, and crews select from the catalog by label. The scheduler
   then loads and evicts models on that GPU on demand.

Two GPU nodes mean two model servers and two ModelServers. To see GPU utilization and
VRAM in the dashboard, deploy the NVIDIA DCGM exporter and point the operator at your
Prometheus. See [Observability](../observability/) and [Models](../../reference/models/).

## Run vLLM on a GPU node

vLLM is the currently preferred engine for an installation: a moot sends overlapping calls
to the same model, and in the [measured results](../../concepts/choosing-a-model/#measured-results)
vLLM served overlapping calls 2.0 to 3.7 times faster than Ollama on the same cards. Install
the model server first, then tell Kubemoot about it: Kubemoot runs no model servers itself.
Ollama stays the engine of the quickstart and a supported alternative
([below](#run-ollama-on-a-gpu-node)).

### Install the servers with the kubemoot-vllm chart

The `kubemoot-vllm` chart installs plain Kubernetes objects and no Kubemoot resources, the
way a community Ollama chart does. Sleep mode needs one vLLM server per model, and the
servers share one accelerator, so the chart runs one pod with a server for each model you
list, a Service with one port per model, and a volume for the weights. Put the pod on the
GPU node, name the models, and give the chart the image by digest (there is no default tag):

```yaml
# values.yaml: two models on one accelerator
template:
  nodeSelector: {kubernetes.io/hostname: gpu-worker}
  tolerations:
    - {key: nvidia.com/gpu, operator: Equal, value: present, effect: NoSchedule}
  container:
    image: vllm/vllm-openai:v0.32.0@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34
    resources:
      requests: {memory: 32Gi, nvidia.com/gpu: 1}
      limits: {memory: 44Gi, nvidia.com/gpu: 1}
weights: {storageClassName: fast, size: 100Gi}
shmSize: 8Gi
sleepLevel: 1
models:
  - servedName: qwen3:14b
    repo: Qwen/Qwen3-14B-AWQ
    revision: 31c69efc
    parameters: 14B
    capabilities: [tool-calling, reasoning]
    args: [--gpu-memory-utilization=0.85, --max-model-len=16384, --enable-auto-tool-choice, --tool-call-parser=hermes]
  - servedName: qwen3:32b
    repo: Qwen/Qwen3-32B-AWQ
    revision: 0499c3ac
    parameters: 32B
    capabilities: [tool-calling, reasoning]
    args: [--gpu-memory-utilization=0.95, --max-model-len=16384, --enable-auto-tool-choice, --tool-call-parser=hermes]
```

```bash
helm install vllm-gpu oci://ghcr.io/kubemoot/charts/kubemoot-vllm -n vllm --create-namespace -f values.yaml
```

What the values mean:

| Value | What it is |
|-------|------------|
| `template` | The pod's `nodeSelector`, `tolerations`, `affinity`, and `imagePullSecrets`, and under `container` the image and `resources`. Name the accelerator in `resources` by its resource name, so any vendor works. |
| `weights` | A ReadWriteOnce volume (`storageClassName`, `size`) for the downloaded weights and vLLM's compile cache. Helm keeps it when you uninstall, so the weights are never downloaded twice by accident; delete the claim yourself to free the space. |
| `shmSize` | The memory-backed `/dev/shm` that vLLM's workers share. It counts against the memory limit. Default `8Gi`. |
| `sleepLevel` | `1` parks sleeping weights in host memory (fast to wake), `2` drops them and reloads from disk. Default `1`. |
| `models` | One server each: `servedName` (`--served-model-name`), `repo`, `revision`, `parameters` and `capabilities`, and the model's own `args` (memory fraction, context length, tool and reasoning parsers). vLLM cannot report a model's parameter count or abilities, so the chart declares them once and publishes them on its Service; the operator reads them into the model catalog, where `parameters` gives the model its size class and `capabilities` become the labels crews select on. Set `tool-calling` only when `args` also turn tool calls on. The chart refuses `--model`, `--served-model-name`, `--revision`, `--port`, `--host`, and `--enable-sleep-mode` in `args`, because it sets them. |

The chart starts the servers one at a time in a single container, because each measures
free accelerator memory when it starts: it starts a server, waits for its `/health`, puts
it to sleep, and starts the next. Every server is asleep when it is done: the launcher never
wakes one. Kubemoot's operator wakes the server a call needs, and puts the awake one to sleep
first, when an agent asks over NATS. The launcher waits on what each server reports, not on a
clock: a sleep is done when the server says it is asleep. A server is dead when its process has
exited, and only then is it restarted the same way, with every other server asleep first.
The launcher never kills a running server, however slowly it answers, because Kubemoot's
adapter may be waking or sleeping it; a running server that does not answer is skipped when
the others are put to sleep, and keeps the pod from being ready until it answers. Each
server's log lines carry its model name. The pod is ready once every server is up and
answering, which takes minutes on the first start while the weights download and the models
compile. A liveness probe reads a heartbeat the launcher touches whenever it makes progress,
so a launcher that is itself stuck is restarted with the pod.

The launcher judges a sleep or wake by what the server reports, never by a clock, with one
safety net. While it waits it watches for any change: a step answered, or, once the steps are
answered, a different state reported. If nothing at all changes for 30 minutes, it stops recording progress and exits, so
the liveness probe or the container restart restarts the pod and frees the accelerator. A wait
that is progressing, such as a slow weight reload, never reaches it. Until then, a wait on a
server that has not answered holds up restarting other servers that exit. A running server that
does not report its sleep state is not put to sleep before another server starts, so its memory
use can make that start fail; the launcher retries.

Set the memory limit above the weights of every model plus `shmSize`; the servers are
ordered by `servedName`, on ports 8000 and up, and adding or removing a model recreates the
pod. The pod meets the `restricted` Pod Security level except `runAsNonRoot`, because the
vLLM image runs as root, so its namespace needs `baseline`.

The install notes print the ModelServer ready to copy. It names the chart's Service, so you
write no endpoints; the operator reads the Service's ports and asks each one which model it
serves. Its `sleepLevel` must equal the release's:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata: {name: vllm-gpu-kubemoot-vllm, namespace: kubemoot}
spec:
  type: vllm
  sleepLevel: 1
  serviceRef: {name: vllm-gpu-kubemoot-vllm, namespace: vllm}
```

The operator chart can create the same ModelServer from its `modelServers` values. No Model
is needed: every model the release serves joins the cluster's model catalog on its own, with
the size and abilities the release's values declare. Add a Model in the operator's namespace
only to give a model labels of your own (data residency, license), to correct what the chart
declares, or to keep a model out of the catalog (`disabled: true`). See
[Models](../../reference/models/#vllm-servers) for the fields. Every server starts asleep. When a
call needs a model, the agent runtime asks the operator to wake it, and the operator puts the
awake one to sleep first, so the first call to a model waits for its wake (seconds).

### Requirements for the node and the server

These apply to the node and to the Kubernetes objects around the server. The chart
already does the ones marked (chart); any other vLLM deployment must do them itself:

- **Use a driver that supports the CUDA version the image is built on.** The node's
  NVIDIA driver must be new enough for the CUDA version of the model server image. In the
  measurement, the vLLM image was built on CUDA 13.0, which needs NVIDIA driver 580 or
  later. The GPU Operator publishes the node's driver in the
  `nvidia.com/cuda.driver-version.full` node label.
- **Leave disk room for the image.** The vLLM image is about 9 GB compressed and larger
  unpacked. A node with a 30 GB disk went into `DiskPressure` while pulling it. Keep model
  weights on network storage (a ReadWriteOnce volume), not on the node disk.
- **Give every engine the same context length.** When several engines serve the same
  crews, set the same context length on each, so a long prompt is not cut on one and
  refused on another. vLLM refuses to start rather than shrink its context: a 4-bit 32B
  model on a 24 GB GPU leaves too little cache for a 32k context, and on a 32 GB GPU it
  needs 95% of GPU memory for one 32k request.
- **Pass AVX through in a VM.** The virtual CPU must expose AVX to the guest, or vLLM
  crashes at start. On Proxmox, set the CPU type to `host`.
- **Do not name the Service `vllm`.** Kubernetes injects service-link environment
  variables into pods, and a Service named `vllm` sets `VLLM_PORT=tcp://...`, which stops
  vLLM. Set `enableServiceLinks: false` on the pod, or name the Service otherwise (chart).
- **Size GPU memory for one full request.** A 32B 4-bit model on a 32 GB GPU needs
  `--gpu-memory-utilization 0.95` to leave room for the cache of one 32k-token request.
  Check that the cache holds at least one request at your context length.
- **Mount a memory-backed `/dev/shm` (chart).** Back it with an `emptyDir` whose `medium` is
  `Memory`.
- **Enable sleep mode to switch models quickly (chart).** Start each server with
  `--enable-sleep-mode` and set `VLLM_SERVER_DEV_MODE=1`. Sleep and wake are development
  endpoints: keep them off any public route and reachable only inside the cluster. See
  [Switching models](../../concepts/choosing-a-model/#switching-models).
- **Cover parked weights in the memory limit.** At sleep level 1 every sleeping model's
  weights sit in host memory, so the container memory limit must cover them. A 14B and a
  32B together needed 44 GiB on a 48 GB node, and a memory-backed `/dev/shm` counts
  against the limit.
- **Allow for start-up time.** From a warm weights volume, start-up took 100 to 220 s,
  mostly CUDA graph capture and compilation. Set probes so Kubernetes waits for it.

## Run Ollama on a GPU node

Ollama is the engine of the [quickstart](../../introduction/quickstart/) and stays supported
for an installation. It suits a host without the memory to park vLLM weights, many models
that share a card and are each used now and then, and CPU-only or small setups (see
[the serving runtime](../../concepts/choosing-a-model/#the-serving-runtime-is-a-separate-choice)).
Deploy it as an ordinary workload pinned to the GPU node that requests `nvidia.com/gpu`, for
example with the community [Ollama Helm chart](https://github.com/otwld/ollama-helm), then
point a `ModelServer` at its Service and list the models the cluster should have. Kubemoot
downloads each listed model that the server lacks:

```yaml
# kubemoot-operator values
modelServers:
  - name: ollama-gpu
    type: ollama
    endpoint: http://ollama.ollama:11434
    models:
      - model: qwen3:14b
```

Only `model` is needed: the operator reads the size, context window and abilities from
Ollama. Models the server already has join the catalog without an entry.

## Worked example: Proxmox and Talos

The Kubemoot reference deployment runs Kubernetes on Talos Linux VMs hosted on Proxmox,
with one consumer GPU passed through to each GPU worker VM. It follows the path above.

**Host (Proxmox).** IOMMU is enabled in firmware and on the kernel command line, each GPU
and its audio function are bound to `vfio-pci`, and each GPU worker VM has the card
attached as a PCIe device on a `q35` machine with OVMF firmware. A GPU is shared between
a Kubernetes worker VM and a Windows VM by running only one of them at a time, draining
the Kubernetes node before stopping it.

**Node image (Talos).** The GPU worker boots from an image built with the Talos Image
Factory that includes two system extensions: the NVIDIA open-source kernel modules and
the NVIDIA container toolkit. Both stay on the image; the toolkit extension provides the
container runtime handler that the kubelet needs to start. The machine configuration
loads the `nvidia`, `nvidia_uvm`, `nvidia_drm`, and `nvidia_modeset` kernel modules,
labels the node (`gpu.nvidia.com/gpu-node: "true"`), and taints it
(`nvidia.com/gpu: present:NoSchedule`).

**Kubernetes (GPU Operator).** The GPU Operator chart runs with the driver and toolkit
components disabled, because the Talos extensions provide them. Its host path for driver
binaries points at the location Talos uses, the device plugin and GPU feature discovery
stay enabled, and MIG is off because consumer cards do not support it. Workloads request
`nvidia.com/gpu` as usual; device injection happens through CDI, so no `nvidia`
RuntimeClass is needed.

```yaml
# GPU Operator values for a node image that already carries the driver and toolkit
driver:
  enabled: false
toolkit:
  enabled: false
hostPaths:
  driverInstallDir: /usr/local
devicePlugin:
  enabled: true
gfd:
  enabled: true
dcgmExporter:
  enabled: false     # deployed separately
mig:
  strategy: none
```

**Kubemoot.** Each GPU worker runs its own model server pinned to the node, and each
has a `ModelServer`. DCGM metrics tell the operator which GPU and how much VRAM each
server has, and the scheduler places models across the two on demand.

Talos documents the extension and operator steps for each of its releases in its
[NVIDIA GPU guide](https://www.talos.dev/latest/talos-guides/configuration/nvidia-gpu/);
follow the page that matches your Talos release.

## Next

- [Install the Operator](../../introduction/installation/) - prerequisites and the model server step.
- [Models](../../reference/models/) - `ModelServer` fields and scheduling.
- [Observability](../observability/) - DCGM metrics and the surfaces that show them.
