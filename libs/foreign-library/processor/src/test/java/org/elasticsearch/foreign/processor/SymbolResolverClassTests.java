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
 * Tests for the {@code symbolResolver} and {@code methodHandleResolver} parameters on
 * {@code @LibrarySpecification}.
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
                    String name = "mylib_" + symbolName;
                    return new ResolvedSymbol(name, lookup.find(name).orElseThrow(
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
     * A valid method handle resolver implementing MethodHandleResolver with a no-arg constructor
     * compiles cleanly.
     */
    public void testValidMethodHandleResolverCompiles() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            class MyMhResolver implements MethodHandleResolver {
                public MyMhResolver() {}
                @Override
                public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = MyMhResolver.class)
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
     * The method handle resolver class must have a public no-arg constructor.
     */
    public void testMethodHandleResolverMissingNoArgConstructorEmitsError() {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            class BadMhResolver implements MethodHandleResolver {
                public BadMhResolver(String config) {}
                @Override
                public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor, Linker linker, Linker.Option... options) {
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = BadMhResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertFalse("Expected compilation to fail when method handle resolver has no no-arg constructor", result.success());
        boolean hasError = result.errors().stream().anyMatch(msg -> msg.contains("must have a public no-arg constructor"));
        assertTrue("Expected error about no-arg constructor but got: " + result.errors(), hasError);
    }

    /**
     * The method handle resolver class must implement MethodHandleResolver. The type bound on
     * the annotation parameter ({@code Class<? extends MethodHandleResolver>}) causes javac to
     * reject a class that doesn't implement the interface before the processor even runs.
     */
    public void testMethodHandleResolverNotImplementingInterfaceEmitsError() {
        String source = """
            package test;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            class BadMhResolver {
                public BadMhResolver() {}
            }
            @LibrarySpecification(name = "testlib", methodHandleResolver = BadMhResolver.class)
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
     * The generated {@code <clinit>} must route MethodHandle creation through the configured
     * {@code methodHandleResolver}, passing it the actual symbol name chosen by the custom
     * {@code symbolResolver}. Initializes the generated class and asserts the custom resolver
     * observed the mangled name.
     */
    public void testCustomMethodHandleResolverIsInvokedWithResolvedName() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.foreign.MemorySegment;
            import java.lang.foreign.SymbolLookup;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            @LibrarySpecification(symbolResolver = MyLib.FakeResolver.class, methodHandleResolver = MyLib.RecordingMhResolver.class)
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);

                class FakeResolver implements SymbolResolver {
                    public FakeResolver() {}
                    @Override
                    public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                        // downcallHandle validates the address is non-NULL; any positive value works.
                        return new ResolvedSymbol(symbolName + "_v2", MemorySegment.ofAddress(1L));
                    }
                }

                class RecordingMhResolver implements MethodHandleResolver {
                    public static volatile String observedName;
                    public RecordingMhResolver() {}
                    @Override
                    public MethodHandle resolve(ResolvedSymbol symbol, FunctionDescriptor descriptor,
                                                Linker linker, Linker.Option... options) {
                        observedName = symbol.name();
                        return linker.downcallHandle(symbol.address(), descriptor, options);
                    }
                }
            }
            """;

        CompilationResult result = compile("test.MyLib", source);

        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        // Initializing the impl class runs <clinit>, which must call RecordingMhResolver.resolve
        // with the name chosen by FakeResolver.
        Class<?> implClass = result.loadClass("test.MyLib$Impl");
        assertNotNull("Generated MyLib$Impl class not found", implClass);

        Class<?> recorderClass = result.loadClass("test.MyLib$RecordingMhResolver");
        String observedName = (String) recorderClass.getDeclaredField("observedName").get(null);
        assertEquals("native_add_v2", observedName);
    }
}
