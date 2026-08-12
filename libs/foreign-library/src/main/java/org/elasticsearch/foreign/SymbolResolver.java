/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign;

import java.lang.foreign.SymbolLookup;

/**
 * Resolves native symbol names to function pointers. Implementations can apply custom lookup
 * strategies such as capability-based fallback, name mangling, or prefix/suffix schemes.
 *
 * <p>The resolver receives the symbol name declared in {@link Function @Function} and a
 * {@link SymbolLookup} for probing the loaded library. It returns a {@link ResolvedSymbol}
 * carrying the actual symbol name that was chosen and its function pointer. The framework then
 * creates the downcall handle via {@link MethodHandleResolver}.
 *
 * <p>Implementing classes must have a public no-arg constructor.
 *
 * <p>Example — a resolver that tries capability-suffixed symbols before falling back to the base name:
 *
 * <pre>{@code
 * public class TieredResolver implements SymbolResolver {
 *     private final int capLevel = detectCapabilityLevel();
 *
 *     @Override
 *     public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
 *         for (int level = capLevel; level >= 1; level--) {
 *             String candidate = symbolName + "_" + level;
 *             var addr = lookup.find(candidate);
 *             if (addr.isPresent()) {
 *                 return new ResolvedSymbol(candidate, addr.get());
 *             }
 *         }
 *         return new ResolvedSymbol(symbolName, lookup.find(symbolName).orElseThrow(() ->
 *             new UnsatisfiedLinkError("Symbol not found: " + symbolName)));
 *     }
 * }
 * }</pre>
 */
@FunctionalInterface
public interface SymbolResolver {
    /**
     * Resolves a native symbol name to its function pointer.
     *
     * @param symbolName the C symbol name from the {@link Function @Function} annotation
     * @param lookup the symbol lookup for the loaded library
     * @return the resolved symbol (must not be null); its address must not be null
     * @throws UnsatisfiedLinkError if no suitable symbol is found
     */
    ResolvedSymbol resolve(String symbolName, SymbolLookup lookup);
}
