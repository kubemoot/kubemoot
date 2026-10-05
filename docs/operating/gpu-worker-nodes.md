---
title: "GPU Worker Nodes"
description: "Give Kubernetes worker nodes NVIDIA GPUs, on bare metal or through VM passthrough, so a Kubemoot ModelProvider can use them."
weight: 4
---

Kubemoot schedules models onto GPUs, but it does not create GPU nodes. This page covers
the general path for making an NVIDIA GPU usable by Kubernetes workloads, whether the
worker node is a bare-metal machine or a virtual machine, and ends with how a Kubemoot
`ModelProvider` then uses the node. A short worked example closes the page.

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

1. Deploy a model server (Ollama) pinned to the GPU node, requesting `nvidia.com/gpu: 1`.
2. Declare one `ModelProvider` pointing at that server's Service.
3. The operator discovers the models, the node, and, when DCGM metrics are available,
   the GPU model and VRAM from the node. The scheduler then loads and evicts models on
   that GPU on demand.

Two GPU nodes mean two model servers and two ModelProviders. To see GPU utilization and
VRAM in the dashboard, deploy the NVIDIA DCGM exporter and point the operator at your
Prometheus. See [Observability](../observability/) and [Models](../../reference/models/).

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

**Kubemoot.** Each GPU worker runs its own Ollama pinned to the node, and each Ollama
has a `ModelProvider`. DCGM metrics tell the operator which GPU and how much VRAM each
provider has, and the scheduler places models across the two on demand.

Talos documents the extension and operator steps for each of its releases in its
[NVIDIA GPU guide](https://www.talos.dev/latest/talos-guides/configuration/nvidia-gpu/);
follow the page that matches your Talos release.

## Next

- [Install the Operator](../../introduction/installation/) - prerequisites and the model server step.
- [Models](../../reference/models/) - `ModelProvider` fields and scheduling.
- [Observability](../observability/) - DCGM metrics and the surfaces that show them.
