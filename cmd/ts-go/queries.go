package main

// Tree-sitter query patterns for Go structural extraction.
// Derived from the tree-sitter-go tags.scm grammar.

// queryFuncDeclarations matches top-level function declarations.
const queryFuncDeclarations = `
(function_declaration
  name: (identifier) @func.name
) @func.decl
`

// queryMethodDeclarations matches method declarations (functions with receivers).
const queryMethodDeclarations = `
(method_declaration
  receiver: (parameter_list) @method.receiver
  name: (field_identifier) @method.name
) @method.decl
`

// queryTypeDeclarations matches type declarations (struct, interface, defined types).
const queryTypeDeclarations = `
(type_declaration
  (type_spec
    name: (type_identifier) @type.name
    type: (_) @type.body
  )
) @type.decl
`

// queryTypeAliases matches type alias declarations (type X = Y).
const queryTypeAliases = `
(type_declaration
  (type_alias
    name: (type_identifier) @alias.name
    type: (_) @alias.body
  )
) @alias.decl
`
