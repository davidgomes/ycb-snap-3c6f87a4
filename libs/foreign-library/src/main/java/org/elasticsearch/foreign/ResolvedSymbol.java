/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign;

import java.lang.foreign.MemorySegment;

/**
 * The result of resolving a native symbol via a {@link SymbolResolver}. Carries both the actual
 * symbol name that was chosen (which may differ from the name declared in
 * {@link Function @Function} when the resolver applies mangling or fallback schemes) and its
 * function pointer, so downstream {@link MethodHandleResolver} implementations can adapt the
 * {@code MethodHandle} based on which symbol variant was selected.
 *
 * @param name the actual native symbol name that was resolved
 * @param address the function pointer for the resolved symbol
 */
public record ResolvedSymbol(String name, MemorySegment address) {}
