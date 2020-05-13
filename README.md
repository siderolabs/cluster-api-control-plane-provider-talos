# project-template-kubernetes-controller

Initializing a new project.

```bash
make init DOMAIN=domain.tld NAMESPACE=template
```

Rename the controller.

Find all instances of `template-controller-manager` and replace it with the new controller name.

Generate a new API.

```bash
kubebuilder create api --group foo --version v1alpha1 --kind Bar --namespaced=<true|false> --controller --resource --make=false --example=false
```

## References

- https://book.kubebuilder.io/reference/markers/crd.html
