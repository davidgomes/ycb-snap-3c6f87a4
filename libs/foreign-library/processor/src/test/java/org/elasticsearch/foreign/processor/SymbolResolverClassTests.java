/*
 * Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
 * or more contributor license agreements. Licensed under the "Elastic License
 * 2.0", the "GNU Affero General Public License v3.0 only", and the "Server Side
 * Public License v 1"; you may not use this file except in compliance with, at
 * your election, the "Elastic License 2.0", the "GNU Affero General Public
 * License v3.0 only", or the "Server Side Public License, v 1".
 */

package org.elasticsearch.foreign.processor;

import org.elasticsearch.core.SuppressForbidden;

import java.lang.invoke.MethodHandle;

/**
 * Tests for the {@code symbolResolver} parameter on {@code @LibrarySpecification}.
 */
@SuppressForbidden(reason = "tests verify private fields of processor-generated classes")
public class SymbolResolverClassTests extends ProcessorTestCase {

    /**
     * A valid resolver implementing SymbolResolver with a no-arg constructor compiles cleanly.
     */
    public void testValidResolverCompiles() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.SymbolLookup;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            class MyResolver implements SymbolResolver {
                public MyResolver() {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    return new ResolvedSymbol(symbolName, lookup.find(symbolName).orElseThrow());
                }
            }
            @LibrarySpecification(name = "testlib", symbolResolver = MyResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        Class<?> implClass = result.loadClassNoInit("test.MyLib$Impl");
        assertNotNull("Generated MyLib$Impl class not found", implClass);

        java.lang.reflect.Field mhField = implClass.getDeclaredField("add$mh");
        assertEquals("add$mh must be a MethodHandle", MethodHandle.class, mhField.getType());
    }

    /**
     * The resolver class must implement SymbolResolver. The type bound on the annotation
     * parameter ({@code Class<? extends SymbolResolver>}) causes javac to reject a class
     * that doesn't implement the interface before the processor even runs.
     */
    public void testResolverNotImplementingInterfaceEmitsError() {
        String source = """
            package test;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            class BadResolver {
                public BadResolver() {}
            }
            @LibrarySpecification(name = "testlib", symbolResolver = BadResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when resolver doesn't implement SymbolResolver", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("cannot be converted to"));
        assertTrue("Expected type mismatch error but got: " + result.errors(), hasError);
    }

    /**
     * The resolver class must have a public no-arg constructor.
     */
    public void testResolverMissingNoArgConstructorEmitsError() {
        String source = """
            package test;
            import java.lang.foreign.SymbolLookup;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            class BadResolver implements SymbolResolver {
                public BadResolver(String config) {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    return new ResolvedSymbol(symbolName, lookup.find(symbolName).orElseThrow());
                }
            }
            @LibrarySpecification(name = "testlib", symbolResolver = BadResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when resolver has no no-arg constructor", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("must have a public no-arg constructor"));
        assertTrue("Expected error about no-arg constructor but got: " + result.errors(), hasError);
    }

    /**
     * Without a custom symbolResolver, the generated code uses DefaultSymbolResolver.
     */
    public void testNoResolverUsesDefault() throws Exception {
        String source = """
            package test;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            @LibrarySpecification(name = "testlib")
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());
        assertNotNull(result.loadClassNoInit("test.MyLib$Impl"));
    }

    /**
     * A resolver that transforms symbol names (prefix mangling) compiles and generates correctly.
     */
    public void testPrefixResolverCompiles() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.SymbolLookup;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            class PrefixResolver implements SymbolResolver {
                public PrefixResolver() {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    String resolvedName = "mylib_" + symbolName;
                    return new ResolvedSymbol(resolvedName, lookup.find(resolvedName).orElseThrow(
                        () -> new UnsatisfiedLinkError(symbolName)));
                }
            }
            @LibrarySpecification(name = "testlib", symbolResolver = PrefixResolver.class)
            public interface MyLib {
                @Function("compress")
                int compress(long src, int len);
                @Function("decompress")
                int decompress(long src, int len);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        Class<?> implClass = result.loadClassNoInit("test.MyLib$Impl");
        assertNotNull(implClass);
        assertNotNull(implClass.getDeclaredField("compress$mh"));
        assertNotNull(implClass.getDeclaredField("decompress$mh"));
    }

    /**
     * A valid method handle resolver with a public no-arg constructor compiles cleanly and is
     * invoked when the generated implementation initializes.
     */
    public void testMethodHandleResolverIsInvoked() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.foreign.MemorySegment;
            import java.lang.foreign.SymbolLookup;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            @LibrarySpecification(
                symbolResolver = ResolverLib.FakeSymbolResolver.class,
                methodHandleResolver = ResolverLib.RecordingMethodHandleResolver.class
            )
            public interface ResolverLib {
                @Function("native_add")
                int add(int a, int b);

                class FakeSymbolResolver implements SymbolResolver {
                    public FakeSymbolResolver() {}

                    public ResolvedSymbol resolve(String name, SymbolLookup lookup) {
                        return new ResolvedSymbol("native_add_v2", MemorySegment.ofAddress(1L));
                    }
                }

                class RecordingMethodHandleResolver implements MethodHandleResolver {
                    public static boolean invoked;

                    public RecordingMethodHandleResolver() {}

                    public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                        invoked = symbol.name().equals("native_add_v2");
                        return linker.downcallHandle(symbol.address(), descriptor, options);
                    }
                }
            }
            """;

        CompilationResult result = compile("test.ResolverLib", source);
        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        assertNotNull(result.loadClass("test.ResolverLib$Impl"));
        Class<?> resolverClass = result.loadClass("test.ResolverLib$RecordingMethodHandleResolver");
        assertTrue(resolverClass.getField("invoked").getBoolean(null));
    }

    /**
     * The method handle resolver class must implement MethodHandleResolver. The type bound on
     * the annotation parameter causes javac to reject a non-implementing class.
     */
    public void testMethodHandleResolverNotImplementingInterfaceEmitsError() {
        String source = """
            package test;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            class BadResolver {
                public BadResolver() {}
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = BadResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when resolver doesn't implement MethodHandleResolver", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("cannot be converted to"));
        assertTrue("Expected type mismatch error but got: " + result.errors(), hasError);
    }

    /**
     * The method handle resolver class must have a public no-arg constructor.
     */
    public void testMethodHandleResolverMissingNoArgConstructorEmitsError() {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            class BadResolver implements MethodHandleResolver {
                public BadResolver(String config) {}
                @Override
                public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = BadResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when resolver has no no-arg constructor", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("must have a public no-arg constructor"));
        assertTrue("Expected error about no-arg constructor but got: " + result.errors(), hasError);
    }
}
