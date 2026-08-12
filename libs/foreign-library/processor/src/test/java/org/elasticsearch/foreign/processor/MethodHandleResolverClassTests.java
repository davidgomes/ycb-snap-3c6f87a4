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
import org.elasticsearch.foreign.MethodHandleResolver;

import java.lang.classfile.ClassFile;
import java.lang.classfile.instruction.InvokeInstruction;
import java.lang.constant.ClassDesc;
import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodType;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Tests for the {@code methodHandleResolver} parameter on {@code @LibrarySpecification}.
 */
@SuppressForbidden(reason = "tests verify private fields of processor-generated classes")
public class MethodHandleResolverClassTests extends ProcessorTestCase {

    /**
     * A valid resolver implementing MethodHandleResolver with a no-arg constructor compiles cleanly.
     */
    public void testValidResolverCompiles() throws Exception {
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
            class MyMhResolver implements MethodHandleResolver {
                public MyMhResolver() {}
                @Override
                public MethodHandle resolve(
                    ResolvedSymbol symbol,
                    FunctionDescriptor descriptor,
                    Linker linker,
                    Linker.Option... options
                ) {
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            class FakeSymbols implements SymbolResolver {
                public FakeSymbols() {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    return new ResolvedSymbol(symbolName, MemorySegment.ofAddress(1L));
                }
            }
            @LibrarySpecification(
                name = "testlib",
                symbolResolver = FakeSymbols.class,
                methodHandleResolver = MyMhResolver.class
            )
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
     * The resolver class must implement MethodHandleResolver. The type bound on the annotation
     * parameter ({@code Class<? extends MethodHandleResolver>}) causes javac to reject a class
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
     * The resolver class must have a public no-arg constructor.
     */
    public void testResolverMissingNoArgConstructorEmitsError() {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.invoke.MethodHandle;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            class BadResolver implements MethodHandleResolver {
                public BadResolver(String config) {}
                @Override
                public MethodHandle resolve(
                    ResolvedSymbol symbol,
                    FunctionDescriptor descriptor,
                    Linker linker,
                    Linker.Option... options
                ) {
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

    /**
     * Without a custom methodHandleResolver, the generated {@code <clinit>} instantiates
     * {@code DefaultMethodHandleResolver} and invokes {@link MethodHandleResolver#resolve}.
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

        Path classFile = result.outputDir().resolve("test/MyLib$Impl.class");
        assertTrue("Generated MyLib$Impl.class not found", Files.exists(classFile));
        assertClinitRoutesThroughMethodHandleResolver(Files.readAllBytes(classFile));
    }

    /**
     * A custom method-handle resolver is instantiated and invoked from generated {@code <clinit>}.
     */
    public void testCustomResolverIsInvokedAtClassInit() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.foreign.MemorySegment;
            import java.lang.foreign.SymbolLookup;
            import java.lang.invoke.MethodHandle;
            import java.util.concurrent.atomic.AtomicInteger;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            class RecordingResolver implements MethodHandleResolver {
                public static final AtomicInteger CALLS = new AtomicInteger();
                public RecordingResolver() {}
                @Override
                public MethodHandle resolve(
                    ResolvedSymbol symbol,
                    FunctionDescriptor descriptor,
                    Linker linker,
                    Linker.Option... options
                ) {
                    CALLS.incrementAndGet();
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            class FakeSymbols implements SymbolResolver {
                public FakeSymbols() {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    return new ResolvedSymbol(symbolName, MemorySegment.ofAddress(1L));
                }
            }
            @LibrarySpecification(
                symbolResolver = FakeSymbols.class,
                methodHandleResolver = RecordingResolver.class
            )
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);
        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        Class<?> implClass = result.loadClass("test.MyLib$Impl");
        assertNotNull(implClass);

        Class<?> recording = Class.forName("test.RecordingResolver", false, implClass.getClassLoader());
        java.lang.reflect.Field callsField = recording.getField("CALLS");
        callsField.setAccessible(true);
        AtomicInteger calls = (AtomicInteger) callsField.get(null);
        assertEquals("custom MethodHandleResolver.resolve must run during <clinit>", 1, calls.get());

        Path classFile = result.outputDir().resolve("test/MyLib$Impl.class");
        assertClinitRoutesThroughMethodHandleResolver(Files.readAllBytes(classFile));
    }

    /**
     * The method-handle resolver observes the symbol name actually chosen by {@link org.elasticsearch.foreign.SymbolResolver},
     * not merely the name from {@code @Function}.
     */
    public void testResolverSeesChosenSymbolName() throws Exception {
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
            class PrefixSymbols implements SymbolResolver {
                public PrefixSymbols() {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    return new ResolvedSymbol("mylib_" + symbolName, MemorySegment.ofAddress(1L));
                }
            }
            class NameCapturingResolver implements MethodHandleResolver {
                public static String seenName;
                public NameCapturingResolver() {}
                @Override
                public MethodHandle resolve(
                    ResolvedSymbol symbol,
                    FunctionDescriptor descriptor,
                    Linker linker,
                    Linker.Option... options
                ) {
                    seenName = symbol.name();
                    return linker.downcallHandle(symbol.address(), descriptor, options);
                }
            }
            @LibrarySpecification(
                symbolResolver = PrefixSymbols.class,
                methodHandleResolver = NameCapturingResolver.class
            )
            public interface MyLib {
                @Function("compress")
                int compress(int len);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);
        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());
        Class<?> implClass = result.loadClass("test.MyLib$Impl");
        assertNotNull(implClass);

        Class<?> capturing = Class.forName("test.NameCapturingResolver", false, implClass.getClassLoader());
        java.lang.reflect.Field seenName = capturing.getField("seenName");
        seenName.setAccessible(true);
        assertEquals("mylib_compress", seenName.get(null));
    }

    /**
     * A custom resolver can apply {@code MethodHandles.insertArguments} after lookup; the stored
     * handle reflects that adaptation.
     */
    public void testResolverCanInsertArguments() throws Exception {
        String source = """
            package test;
            import java.lang.foreign.FunctionDescriptor;
            import java.lang.foreign.Linker;
            import java.lang.foreign.MemorySegment;
            import java.lang.foreign.SymbolLookup;
            import java.lang.invoke.MethodHandle;
            import java.lang.invoke.MethodHandles;
            import org.elasticsearch.foreign.Function;
            import org.elasticsearch.foreign.LibrarySpecification;
            import org.elasticsearch.foreign.MethodHandleResolver;
            import org.elasticsearch.foreign.ResolvedSymbol;
            import org.elasticsearch.foreign.SymbolResolver;
            class FakeSymbols implements SymbolResolver {
                public FakeSymbols() {}
                @Override
                public ResolvedSymbol resolve(String symbolName, SymbolLookup lookup) {
                    return new ResolvedSymbol(symbolName, MemorySegment.ofAddress(1L));
                }
            }
            class InsertingResolver implements MethodHandleResolver {
                public InsertingResolver() {}
                @Override
                public MethodHandle resolve(
                    ResolvedSymbol symbol,
                    FunctionDescriptor descriptor,
                    Linker linker,
                    Linker.Option... options
                ) {
                    MethodHandle mh = linker.downcallHandle(symbol.address(), descriptor, options);
                    return MethodHandles.insertArguments(mh, 1, 1);
                }
            }
            @LibrarySpecification(
                symbolResolver = FakeSymbols.class,
                methodHandleResolver = InsertingResolver.class
            )
            public interface MyLib {
                @Function("native_add")
                int add(int a, int b);
            }
            """;

        CompilationResult result = compile("test.MyLib", source);
        assertTrue("Expected compilation to succeed but got errors: " + result.errors(), result.success());

        Class<?> implClass = result.loadClass("test.MyLib$Impl");
        java.lang.reflect.Field mhField = implClass.getDeclaredField("add$mh");
        mhField.setAccessible(true);
        MethodHandle mh = (MethodHandle) mhField.get(null);
        assertEquals(MethodType.methodType(int.class, int.class), mh.type());
    }

    /**
     * Generated {@code <clinit>} must call {@code MethodHandleResolver.resolve} and must not
     * invoke {@code Linker.downcallHandle} directly.
     */
    private static void assertClinitRoutesThroughMethodHandleResolver(byte[] classBytes) {
        var clinit = ClassFile.of()
            .parse(classBytes)
            .methods()
            .stream()
            .filter(m -> m.methodName().equalsString("<clinit>"))
            .findFirst();
        assertTrue("<clinit> not found", clinit.isPresent());

        List<InvokeInstruction> invokes = clinit.get()
            .code()
            .stream()
            .flatMap(ca -> ca.elementStream())
            .filter(e -> e instanceof InvokeInstruction)
            .map(e -> (InvokeInstruction) e)
            .toList();

        ClassDesc mhResolver = ClassDesc.of("org.elasticsearch.foreign.MethodHandleResolver");
        boolean callsHook = invokes.stream()
            .anyMatch(inv -> inv.name().equalsString("resolve") && inv.owner().asSymbol().equals(mhResolver));
        assertTrue("Generated <clinit> must invoke MethodHandleResolver.resolve", callsHook);

        ClassDesc linker = ClassDesc.of("java.lang.foreign.Linker");
        boolean callsLinkerDowncall = invokes.stream()
            .anyMatch(inv -> inv.name().equalsString("downcallHandle") && inv.owner().asSymbol().equals(linker));
        assertFalse("Generated <clinit> must not call Linker.downcallHandle directly", callsLinkerDowncall);
    }
}
