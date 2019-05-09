Terraform Compiler Provider
-------

<a href="http://www.wtfpl.net/"><img
       src="http://www.wtfpl.net/wp-content/uploads/2012/12/wtfpl-badge-4.png"
       width="80" height="15" alt="WTFPL" /></a>

:warning: You shouldn't use this provider

Terraform provider for compiling your code in place

Requirements
------------

- [Docker](https://www.docker.com/)

Usages
------

```hcl-terraform
provider "compiler" {
}

resource "compile" "default" {
    provider = "compiler"
    image    = "python3.7"
    input    = "${path.module}/input"
    output   = "${path.module}/output"
    filename = "lambda.zip"
}
```