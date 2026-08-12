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
 * The outcome of resolving a native symbol via {@link SymbolResolver}.
 *
 * <p>{@code name} is the actual symbol name that was chosen, which may differ from the symbol
 * name requested by the {@link Function @Function} annotation — for example when a
 * {@link SymbolResolver} tries capability-suffixed variants before falling back to the base name.
 * Passing this along lets a {@link MethodHandleResolver} tailor {@code MethodHandle} creation to
 * whichever variant was actually resolved.
 *
 * @param name the actual symbol name that was resolved
 * @param address the function pointer for the resolved symbol
 */
public record ResolvedSymbol(String name, MemorySegment address) {}
