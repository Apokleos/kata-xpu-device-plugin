# Kata xPU Device Plugin to assign xPUs to Kata Containers

> Currently, the DP will only support KataContainers v3.8 or newer.

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Architecture](#architecture)


## Overview

Kata-xpu-device-plugin Enhanced Device Plugin is heavily inspired by the [`kubevirt-gpu-device-plugin`](https://github.com/NVIDIA/kubevirt-gpu-device-plugin). It's designed to support for various accelerated computing devices (xPUs), including GPUs, within the Kata Containers. By integrating with the [`Container Device Interface (CDI)`](https://github.com/cncf-tags/container-device-interface), the plugin simplifies the communication of allocated device information and enhances data management efficiency, thereby better addressing the growing demand for heterogeneous computing.


## Features

- Discovers xPUs which are bound to VFIO-PCI driver and exposes them as devices available to be attached to VM in pass through mode.
- Supports Container Device Interface(CDI).

## Prerequisites

- xPUs drivers should be unbound from host with vfio-pci driver and vfio devices generated. 


## Architecture

![architecture overview](docs/image.png)

